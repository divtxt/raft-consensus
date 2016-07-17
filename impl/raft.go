// Package raft is an implementation of the Raft consensus protocol.
//
// Call NewConsensusModule with appropriate parameters to start an instance.
// Incoming RPC calls can then be sent to it using the ProcessRpc...Async
// methods.
//
// You will have to provide implementations of the following interfaces:
//
//  - RaftPersistentState
//  - LogAndStateMachine
//  - RpcService
//
// Notes for implementers of these interfaces:
//
// - Concurrency: a ConsensusModule will only ever call the methods of these
// interfaces from it's single goroutine.
//
// - Errors: all errors should be checked and returned. This includes both
// invalid parameters sent by the consensus module and internal errors in the
// implementation. Note that any error will shutdown the ConsensusModule.
//
package impl

import (
	"errors"
	. "github.com/divtxt/raft"
	"github.com/divtxt/raft/config"
	"github.com/divtxt/raft/consensus"
	"sync/atomic"
	"time"
)

const (
	RPC_CHANNEL_BUFFER_SIZE = 100
)

// A ConsensusModule is an active Raft consensus module implementation.
type ConsensusModule struct {
	passiveConsensusModule *consensus.PassiveConsensusModule

	// -- External components - these fields meant to be immutable
	rpcService RpcService

	// -- State - these fields may be accessed concurrently
	stopped int32

	// -- Channels
	runnableChannel chan func() error
	ticker          *time.Ticker

	// -- Control
	stopSignal chan struct{}
	stopError  *atomic.Value
}

// Allocate and initialize a ConsensusModule with the given components and
// settings.
//
// All parameters are required.
// timeSettings is checked using ValidateTimeSettings().
//
// A goroutine that handles consensus processing is created. All processing is done by this
// goroutine. The various ...Async() methods send their work to this goroutine via an internal
// channel of size RPC_CHANNEL_BUFFER_SIZE.
//
// The various ...Async() methods follow these conventions:
//
//  - Each call immediately returns a single-use channel. The result will be sent later via this
//    channel when the actual processing of that request is complete.
//  - If the processing encounters an error, the ConsensusModule will stop. No reply will be sent
//    on the reply channel.
//  - If the internal processing channel is full, the ...Async() method call will pretend to
//    succeed but no reply will be sent on the returned reply channel.
//  - If the ConsensusModule is stopped, the ...Async() method call will pretend to succeed but
//    no reply will be sent on the reply channel.
//
func NewConsensusModule(
	raftPersistentState RaftPersistentState,
	lasm LogAndStateMachine,
	rpcService RpcService,
	clusterInfo *config.ClusterInfo,
	timeSettings config.TimeSettings,
) (*ConsensusModule, error) {
	runnableChannel := make(chan func() error, RPC_CHANNEL_BUFFER_SIZE)
	ticker := time.NewTicker(timeSettings.TickerDuration)
	now := time.Now()

	cm := &ConsensusModule{
		nil, // temp value, to be replaced before goroutine start

		// -- External components
		rpcService,

		// -- State
		0,

		// -- Channels
		runnableChannel,
		ticker,

		// -- Control
		make(chan struct{}, 1),
		&atomic.Value{},
	}

	pcm, err := consensus.NewPassiveConsensusModule(
		raftPersistentState,
		lasm,
		cm,
		clusterInfo,
		timeSettings.ElectionTimeoutLow,
		now,
	)
	if err != nil {
		return nil, err
	}

	// we can only set the value here because it's a cyclic reference
	cm.passiveConsensusModule = pcm

	// Start the go routine
	go cm.processor()

	return cm, nil
}

// Check if the ConsensusModule is stopped.
func (cm *ConsensusModule) IsStopped() bool {
	return atomic.LoadInt32(&cm.stopped) != 0
}

// Stop the ConsensusModule asynchronously.
//
// This will effectively stop the goroutine that does the processing.
// This is safe to call multiple times, even if the goroutine has already stopped.
func (cm *ConsensusModule) StopAsync() {
	select {
	case cm.stopSignal <- struct{}{}:
	default:
	}
}

// Get the error that stopped the ConsensusModule goroutine.
//
// The value will be nil if the goroutine is not stopped, or if it stopped
// without an error.
func (cm *ConsensusModule) GetStopError() error {
	stopErr := cm.stopError.Load()
	if stopErr != nil {
		return stopErr.(error)
	}
	return nil
}

// Get the current server state.
func (cm *ConsensusModule) GetServerState() ServerState {
	return cm.passiveConsensusModule.GetServerState()
}

// Process the given RpcAppendEntries message from the given peer
// asynchronously.
//
// This method sends the RPC message to the ConsensusModule's goroutine.
// The RPC reply will be sent later on the returned channel.
//
// See the RpcService interface for outgoing RPC.
//
// See the notes on NewConsensusModule() for more details about this method's behavior.
func (cm *ConsensusModule) ProcessRpcAppendEntriesAsync(
	from ServerId,
	rpc *RpcAppendEntries,
) <-chan *RpcAppendEntriesReply {
	replyChan := make(chan *RpcAppendEntriesReply, 1)
	f := func() error {
		now := time.Now()

		rpcReply, err := cm.passiveConsensusModule.Rpc_RpcAppendEntries(from, rpc, now)
		if err != nil {
			return err
		}

		select {
		case replyChan <- rpcReply:
			return nil
		default:
			// theoretically unreachable as we make a buffered channel of
			// capacity 1 and this is the one send to it
			return errors.New("FATAL: replyChan is nil or wants to block")
		}
	}
	cm.runInProcessor(f)
	return replyChan
}

// Process the given RpcRequestVote message from the given peer
// asynchronously.
//
// This method sends the RPC message to the ConsensusModule's goroutine.
// The RPC reply will be sent later on the returned channel.
//
// See the RpcService interface for outgoing RPC.
//
// See the notes on NewConsensusModule() for more details about this method's behavior.
func (cm *ConsensusModule) ProcessRpcRequestVoteAsync(
	from ServerId,
	rpc *RpcRequestVote,
) <-chan *RpcRequestVoteReply {
	replyChan := make(chan *RpcRequestVoteReply, 1)
	f := func() error {
		now := time.Now()

		rpcReply, err := cm.passiveConsensusModule.Rpc_RpcRequestVote(from, rpc, now)
		if err != nil {
			return err
		}

		select {
		case replyChan <- rpcReply:
		default:
			// theoretically unreachable as we make a buffered channel of
			// capacity 1 and this is the one send to it
			return errors.New("FATAL: replyChan is nil or wants to block")
		}

		return nil
	}
	cm.runInProcessor(f)
	return replyChan
}

// Append the given command as an entry in the log.
//
// This can only be done if the ConsensusModule is in LEADER state.
//
// The command is considered to be in unserialized form.  The value is opaque to the
// ConsensusModule and will be sent as is to LogAndStateMachine.AppendEntry()
//
// This method sends the command to the ConsensusModule's goroutine.
// The reply will be sent later on the returned channel when the command has been appended.
//
// The reply will be the value returned by AppendEntry(), or nil if this ConsensusModule is
// not the leader.
//
// Here, we intentionally punt on some of the leader details, specifically
// most of:
//
// #RFS-L2: If command received from client: append entry to local log,
// respond after entry applied to state machine (#5.3)
//
// We choose not to deal with the client directly. You must implement the
// interaction with clients and waiting the entry to be applied to the state
// machine. (see delegation of lastApplied to the Log interface)
//
// See the notes on NewConsensusModule() for more details about this method's behavior.
func (cm *ConsensusModule) AppendCommandAsync(
	rawCommand interface{},
) <-chan interface{} {
	replyChan := make(chan interface{}, 1)
	f := func() error {
		result, err := cm.passiveConsensusModule.AppendCommand(rawCommand)
		if err != nil {
			return err
		}

		select {
		case replyChan <- result:
		default:
			// theoretically unreachable as we make a buffered channel of
			// capacity 1 and this is the one send to it
			return errors.New("FATAL: replyChan is nil or wants to block")
		}

		return nil
	}
	cm.runInProcessor(f)
	return replyChan
}

// -- protected methods

// Implement RpcSendOnly.SendOnlyRpcAppendEntriesAsync to bridge to
// RpcService.SendRpcAppendEntriesAsync() with a closure callback.
func (cm *ConsensusModule) SendOnlyRpcAppendEntriesAsync(
	toServer ServerId,
	rpc *RpcAppendEntries,
) error {
	replyAsync := func(rpcReply *RpcAppendEntriesReply) {
		// Process the given RPC reply message from the given peer
		// asynchronously.
		f := func() error {
			return cm.passiveConsensusModule.RpcReply_RpcAppendEntriesReply(toServer, rpc, rpcReply)
		}
		cm.runInProcessor(f)
	}
	return cm.rpcService.SendRpcAppendEntriesAsync(toServer, rpc, replyAsync)
}

// Implement RpcSendOnly.SendOnlyRpcRequestVoteAsync to bridge to
// RpcService.SendRpcRequestVoteAsync() with a closure callback.
func (cm *ConsensusModule) SendOnlyRpcRequestVoteAsync(
	toServer ServerId,
	rpc *RpcRequestVote,
) error {
	replyAsync := func(rpcReply *RpcRequestVoteReply) {
		// Process the given RPC reply message from the given peer
		// asynchronously.
		f := func() error {
			return cm.passiveConsensusModule.RpcReply_RpcRequestVoteReply(toServer, rpc, rpcReply)
		}
		cm.runInProcessor(f)
	}
	return cm.rpcService.SendRpcRequestVoteAsync(toServer, rpc, replyAsync)
}

func (cm *ConsensusModule) processor() {
	var stopErr error = nil

loop:
	for {
		select {
		case runnable, ok := <-cm.runnableChannel:
			if !ok {
				// theoretically unreachable as we don't close the channel
				// til shutdown
				stopErr = errors.New("FATAL: runnableChannel closed")
				break loop
			}
			err := runnable()
			if err != nil {
				stopErr = err
				break loop
			}
		case _, ok := <-cm.ticker.C:
			if !ok {
				// theoretically unreachable as we don't stop the timer til shutdown
				stopErr = errors.New("FATAL: ticker channel closed")
				break loop
			}
			// Get a fresh now since the ticker's now could have been waiting
			now := time.Now()
			err := cm.passiveConsensusModule.Tick(now)
			if err != nil {
				stopErr = err
				break loop
			}
		case <-cm.stopSignal:
			break loop
		}
	}

	// Save error if needed
	if stopErr != nil {
		cm.stopError.Store(stopErr)
	}
	// Mark the server as stopped
	atomic.StoreInt32(&cm.stopped, 1)
	// Clean up things
	cm.runnableChannel = nil // don't close channel - avoids sender panics (rpc callbacks)
	cm.ticker.Stop()
}

func (cm *ConsensusModule) runInProcessor(f func() error) {
	select {
	case cm.runnableChannel <- f:
	default:
	}
}