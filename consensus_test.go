package raft

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testServerId = "s1"
	// Note: value for tests based on Figure 7
	testCurrentTerm = 8

	testTickerDuration     = 15 * time.Millisecond
	testElectionTimeoutLow = 150 * time.Millisecond

	testSleepToLetGoroutineRun = 3 * time.Millisecond
	testSleepJustMoreThanATick = testTickerDuration + testSleepToLetGoroutineRun

	// because Sleep(testSleepToLetGoroutineRun) can take a bit longer
	testElectionTimeoutFuzz = testSleepToLetGoroutineRun + (time.Millisecond * 3 / 2)
)

var testPeerIds = []ServerId{"s2", "s3", "s4", "s5"}

func setupTestFollower(t *testing.T, logTerms []TermNo) *ConsensusModule {
	cm, _ := setupTestFollowerR2(t, logTerms)
	return cm
}

func setupTestFollowerR2(
	t *testing.T,
	logTerms []TermNo,
) (*ConsensusModule, *mockRpcSender) {
	ps := newIMPSWithCurrentTerm(testCurrentTerm)
	imle := newIMLEWithDummyCommands(logTerms)
	mrs := newMockRpcSender()
	ts := TimeSettings{testTickerDuration, testElectionTimeoutLow}
	cm := NewConsensusModule(ps, imle, mrs, testServerId, testPeerIds, ts)
	if cm == nil {
		t.Fatal()
	}
	return cm, mrs
}

func (cm *ConsensusModule) stopAsyncWithRecover() (e interface{}) {
	defer func() {
		e = recover()
	}()
	cm.StopAsync()
	return nil
}

func (cm *ConsensusModule) stopAndCheckError() {
	var callersError, stopAsyncError, stopStatusError, stopError interface{}

	callersError = recover()

	stopAsyncError = cm.stopAsyncWithRecover()

	time.Sleep(testSleepToLetGoroutineRun)
	if !cm.IsStopped() {
		stopStatusError = "Timeout waiting for stop!"
	}

	stopError = cm.GetStopError()

	if stopAsyncError == nil && stopStatusError == nil && stopError == nil {
		if callersError != nil {
			panic(callersError)
		} else {
			return
		}
	} else {
		errs := [...]interface{}{
			callersError,
			stopAsyncError,
			stopStatusError,
			stopError,
		}
		panic(fmt.Sprintf("%v", errs))
	}
}

// #5.2-p1s2: When servers start up, they begin as followers
func TestCMStartsAsFollower(t *testing.T) {
	cm := setupTestFollower(t, nil)
	defer cm.stopAndCheckError()

	if cm.GetServerState() != FOLLOWER {
		t.Fatal()
	}
}

func TestCMStop(t *testing.T) {
	cm := setupTestFollower(t, nil)

	if cm.IsStopped() {
		t.Error()
	}

	cm.StopAsync()
	time.Sleep(testSleepToLetGoroutineRun)

	if !cm.IsStopped() {
		t.Error()
	}
	if cm.GetStopError() != nil {
		t.Error()
	}
}

func TestCMUnknownRpcTypeStopsCM(t *testing.T) {
	cm := setupTestFollower(t, nil)

	if cm.IsStopped() {
		t.Error()
	}

	cm.ProcessRpcAsync("s2", &struct{ int }{42})
	time.Sleep(testSleepToLetGoroutineRun)

	if !cm.IsStopped() {
		cm.StopAsync()
		t.Fatal()
	}

	e := cm.GetStopError()
	if e != "FATAL: unknown rpc type: *struct { int }" {
		t.Error(e)
	}
}

func TestCMSetServerStateBadServerStatePanics(t *testing.T) {
	cm := setupTestFollower(t, nil)
	defer cm.stopAndCheckError()

	defer func() {
		if r := recover(); r != "FATAL: unknown ServerState: 42" {
			t.Error(r)
		}
	}()

	cm.setServerState(42)
}

func TestCMBadServerStateStopsCM(t *testing.T) {
	cm := setupTestFollower(t, nil)

	if cm.IsStopped() {
		t.Error()
	}

	atomic.StoreUint32((*uint32)(&cm.serverState), 42)
	time.Sleep(testSleepJustMoreThanATick)

	if !cm.IsStopped() {
		cm.StopAsync()
		t.Fatal()
	}

	e := cm.GetStopError()
	if e != "FATAL: unknown ServerState: 42" {
		t.Error(e)
	}
}

// #5.2-p1s5: If a follower receives no communication over a period of time
// called the election timeout, then it assumes there is no viable leader
// and begins an election to choose a new leader.
// #5.2-p2s1: To begin an election, a follower increments its current term
// and transitions to candidate state.
// #5.2-p2s2: It then votes for itself and issues RequestVote RPCs in parallel
// to each of the other servers in the cluster.
// #5.2-p6s2: ..., election timeouts are chosen randomly from a fixed
// interval (e.g., 150-300ms)
func testCMFollowerStartsElectionOnElectionTimeout(
	t *testing.T,
	cm *ConsensusModule,
	mrs *mockRpcSender,
) {

	if cm.GetServerState() != FOLLOWER {
		t.Fatal()
	}
	if cm.persistentState.GetVotedFor() != "" {
		t.Fatal()
	}
	if cm.currentElectionTimeout < testElectionTimeoutLow || cm.currentElectionTimeout > 2*testElectionTimeoutLow {
		t.Fatal()
	}

	// Test that a tick before election timeout causes no state change.
	time.Sleep(testSleepJustMoreThanATick)
	if cm.persistentState.GetCurrentTerm() != testCurrentTerm {
		t.Fatal()
	}
	if cm.GetServerState() != FOLLOWER {
		t.Fatal()
	}

	testCMFollowerStartsElectionOnElectionTimeout_Part2(t, cm, mrs, testCurrentTerm+1)
}

func testCMFollowerStartsElectionOnElectionTimeout_Part2(
	t *testing.T,
	cm *ConsensusModule,
	mrs *mockRpcSender,
	expectedNewTerm TermNo,
) {

	// Test that election timeout causes a new election
	time.Sleep(cm.currentElectionTimeout)
	if cm.persistentState.GetCurrentTerm() != expectedNewTerm {
		t.Fatal(expectedNewTerm, cm.persistentState.GetCurrentTerm())
	}
	if cm.GetServerState() != CANDIDATE {
		t.Fatal()
	}
	// candidate has voted for itself
	if cm.persistentState.GetVotedFor() != testServerId {
		t.Fatal()
	}

	// candidate has issued RequestVote RPCs to all other servers.

	sentRpcs := mrs.getAllSortedByToServer()

	if len(sentRpcs) != len(testPeerIds) {
		t.Error()
	}

	lastLogIndex := cm.log.getIndexOfLastEntry()
	var lastLogTerm TermNo = 0
	if lastLogIndex > 0 {
		lastLogTerm = cm.log.getTermAtIndex(lastLogIndex)
	}

	expectedRpc := &RpcRequestVote{expectedNewTerm, lastLogIndex, lastLogTerm}

	for i, peerId := range testPeerIds {
		sentRpc := sentRpcs[i]
		if sentRpc.toServer != peerId {
			t.Error()
		}
		if !reflect.DeepEqual(sentRpc.rpc, expectedRpc) {
			t.Fatal(sentRpc.rpc, expectedRpc)
		}
	}
}

func TestCMFollowerStartsElectionOnElectionTimeout_EmptyLog(t *testing.T) {
	cm, mrs := setupTestFollowerR2(t, nil)
	defer cm.stopAndCheckError()

	testCMFollowerStartsElectionOnElectionTimeout(t, cm, mrs)
}

func TestCMFollowerStartsElectionOnElectionTimeout_NonEmptyLog(t *testing.T) {
	// Log with 10 entries with terms as shown in Figure 7, leader line
	terms := testLogTerms_Figure7LeaderLine()
	cm, mrs := setupTestFollowerR2(t, terms)
	defer cm.stopAndCheckError()

	testCMFollowerStartsElectionOnElectionTimeout(t, cm, mrs)
}
