package raft

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

const (
	testThisServerId = "s1"

	// Note: value for tests based on Figure 7
	// Start as follower at term 7 so that leader will be at term 8
	testCurrentTerm = 7

	testTickerDuration     = 15 * time.Millisecond
	testElectionTimeoutLow = 150 * time.Millisecond

	testSleepToLetGoroutineRun = 3 * time.Millisecond
	testSleepJustMoreThanATick = testTickerDuration + testSleepToLetGoroutineRun

	testMaxEntriesPerAppendEntry = 3
)

var testAllServerIds = []ServerId{testThisServerId, "s2", "s3", "s4", "s5"}

func setupManagedConsensusModule(t *testing.T, logTerms []TermNo) *managedConsensusModule {
	mcm, _ := setupManagedConsensusModuleR2(t, logTerms)
	return mcm
}

func setupManagedConsensusModuleR2(
	t *testing.T,
	logTerms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	ps := newIMPSWithCurrentTerm(testCurrentTerm)
	imle := newIMLEWithDummyCommands(logTerms)
	mrs := newMockRpcSender()
	ci := NewClusterInfo(testAllServerIds, testThisServerId)
	cm, now := newPassiveConsensusModule(
		ps,
		imle,
		mrs,
		ci,
		testElectionTimeoutLow,
		testMaxEntriesPerAppendEntry,
	)
	if cm == nil {
		t.Fatal()
	}
	// Bias simulated clock to avoid exact time matches
	now = now.Add(testSleepToLetGoroutineRun)
	mcm := &managedConsensusModule{cm, now}
	return mcm, mrs
}

// #5.2-p1s2: When servers start up, they begin as followers
func TestCM_InitialState(t *testing.T) {
	mcm := setupManagedConsensusModule(t, nil)

	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}

	// Volatile state on all servers
	if mcm.pcm.volatileState != (volatileState{0}) {
		t.Fatal()
	}
}

func test_ExpectPanic(t *testing.T, f func(), expectedRecover interface{}) {
	skipRecover := false
	defer func() {
		if !skipRecover {
			if r := recover(); r != expectedRecover {
				t.Fatal(fmt.Sprintf("Expected panic: %v; got: %v", expectedRecover, r))
			}
		}
	}()

	f()
	skipRecover = true
	t.Fatal(fmt.Sprintf("Expected panic: %v", expectedRecover))
}

func test_ExpectPanicAnyRecover(t *testing.T, f func()) {
	skipRecover := false
	defer func() {
		if !skipRecover {
			if r := recover(); r == nil {
				t.Fatal("Expected panic, but got panic with recover of nil")
			}
		}
	}()

	f()
	skipRecover = true
	t.Fatal("Expected panic")
}

func testCM_setupMCMAndExpectPanicFor(
	t *testing.T,
	f func(*managedConsensusModule),
	expectedRecover interface{},
) {
	mcm := setupManagedConsensusModule(t, nil)
	test_ExpectPanic(
		t,
		func() {
			f(mcm)
		},
		expectedRecover,
	)
}

func TestCM_UnknownRpcTypePanics(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			mcm.rpc("s2", &struct{ int }{42})
		},
		"FATAL: unknown rpc type: *struct { int } from: s2",
	)
}

func TestCM_RpcReply_UnknownRpcTypePanics(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			mcm.pcm.rpcReply("s2", &struct{ int }{42}, &struct{ int }{42})
		},
		"FATAL: unknown rpc type: *struct { int } from: s2",
	)
}

func TestCM_RpcReply_UnknownRpcReplyTypePanics(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			sentRpc := &RpcRequestVote{1, 0, 0}
			mcm.pcm.rpcReply("s2", sentRpc, &RpcAppendEntriesReply{1, false})
		},
		"FATAL: mismatched rpcReply type: *raft.RpcAppendEntriesReply from: s2 - expected *raft.RpcRequestVoteReply",
	)
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			sentRpc := &RpcAppendEntries{1, 0, 0, nil, 0}
			mcm.pcm.rpcReply("s2", sentRpc, &RpcRequestVoteReply{1, false})
		},
		"FATAL: mismatched rpcReply type: *raft.RpcRequestVoteReply from: s2 - expected *raft.RpcAppendEntriesReply",
	)
}

func TestCM_SetServerStateBadServerStatePanics(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			mcm.pcm._setServerState(42)
		},
		"FATAL: unknown ServerState: 42",
	)
}

func TestCM_BadServerStatePanicsTick(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			mcm.pcm._unsafe_serverState = 42

			mcm.tick()
		},
		"FATAL: unknown ServerState: 42",
	)
}

func TestCM_BadServerStatePanicsRpc(t *testing.T) {
	testCM_setupMCMAndExpectPanicFor(
		t,
		func(mcm *managedConsensusModule) {
			mcm.pcm._unsafe_serverState = 42

			requestVote := &RpcRequestVote{1, 0, 0}

			mcm.rpc("s2", requestVote)
		},
		"FATAL: unknown ServerState: 42",
	)
}

// #RFS-F2: If election timeout elapses without receiving
// AppendEntries RPC from current leader or granting vote
// to candidate: convert to candidate
// #5.2-p1s5: If a follower receives no communication over a period of time
// called the election timeout, then it assumes there is no viable leader
// and begins an election to choose a new leader.
// #RFS-C1: On conversion to candidate, start election:
// Increment currentTerm; Vote for self; Send RequestVote RPCs
// to all other servers; Reset election timer
// #5.2-p2s1: To begin an election, a follower increments its current term
// and transitions to candidate state.
// #5.2-p2s2: It then votes for itself and issues RequestVote RPCs in parallel
// to each of the other servers in the cluster.
// #5.2-p6s2: ..., election timeouts are chosen randomly from a fixed
// interval (e.g., 150-300ms)
func testCM_Follower_StartsElectionOnElectionTimeout(
	t *testing.T,
	mcm *managedConsensusModule,
	mrs *mockRpcSender,
) {

	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}
	if mcm.pcm.persistentState.GetVotedFor() != "" {
		t.Fatal()
	}

	// Test that a tick before election timeout causes no state change.
	mcm.tick()
	if mcm.pcm.persistentState.GetCurrentTerm() != testCurrentTerm {
		t.Fatal()
	}
	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}

	testCM_FollowerOrCandidate_StartsElectionOnElectionTimeout_Part2(t, mcm, mrs, testCurrentTerm+1)
}

func testCM_FollowerOrCandidate_StartsElectionOnElectionTimeout_Part2(
	t *testing.T,
	mcm *managedConsensusModule,
	mrs *mockRpcSender,
	expectedNewTerm TermNo,
) {
	timeout1 := mcm.pcm.electionTimeoutTracker.currentElectionTimeout
	// Test that election timeout causes a new election
	mcm.tickTilElectionTimeout()
	if mcm.pcm.persistentState.GetCurrentTerm() != expectedNewTerm {
		t.Fatal(expectedNewTerm, mcm.pcm.persistentState.GetCurrentTerm())
	}
	if mcm.pcm.getServerState() != CANDIDATE {
		t.Fatal()
	}
	// candidate has voted for itself
	if mcm.pcm.persistentState.GetVotedFor() != testThisServerId {
		t.Fatal()
	}
	// a new election timeout was chosen
	// Playing the odds here :P
	if mcm.pcm.electionTimeoutTracker.currentElectionTimeout == timeout1 {
		t.Fatal()
	}
	// candidate state is fresh
	expectedCvs := newCandidateVolatileState(mcm.pcm.clusterInfo)
	if !reflect.DeepEqual(mcm.pcm.candidateVolatileState, expectedCvs) {
		t.Fatal()
	}

	// candidate has issued RequestVote RPCs to all other servers.
	lastLogIndex, lastLogTerm := GetIndexAndTermOfLastEntry(mcm.pcm.log)
	expectedRpc := &RpcRequestVote{expectedNewTerm, lastLogIndex, lastLogTerm}
	expectedRpcs := []mockSentRpc{
		{"s2", expectedRpc},
		{"s3", expectedRpc},
		{"s4", expectedRpc},
		{"s5", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

func TestCM_Follower_StartsElectionOnElectionTimeout_EmptyLog(t *testing.T) {
	mcm, mrs := setupManagedConsensusModuleR2(t, nil)
	testCM_Follower_StartsElectionOnElectionTimeout(t, mcm, mrs)
}

// #RFS-L1b: repeat during idle periods to prevent election timeout (#5.2)
func TestCM_Leader_SendEmptyAppendEntriesDuringIdlePeriods(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine(t)
	serverTerm := mcm.pcm.persistentState.GetCurrentTerm()

	mrs.checkSentRpcs(t, []mockSentRpc{})

	mcm.tick()

	testIsLeaderWithTermAndSentEmptyAppendEntries(t, mcm, mrs, serverTerm)
}

// #RFS-L3.0: If last log index >= nextIndex for a follower: send
// AppendEntries RPC with log entries starting at nextIndex
func TestCM_Leader_TickSendsAppendEntriesWithLogEntries(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine_WithUpToDatePeers(t)
	serverTerm := mcm.pcm.persistentState.GetCurrentTerm()

	// repatch some peers as not caught up
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 9)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s5", 7)

	// tick should trigger check & appropriate sends
	mcm.tick()

	expectedRpcEmpty := &RpcAppendEntries{
		serverTerm,
		10,
		6,
		[]LogEntry{},
		0, // TODO: tests for this?!
	}
	expectedRpcS2 := &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{
			{6, Command("c10")},
		},
		0, // TODO: tests for this?!
	}
	expectedRpcS5 := &RpcAppendEntries{
		serverTerm,
		7,
		5,
		[]LogEntry{
			{6, Command("c8")},
			{6, Command("c9")},
			{6, Command("c10")},
		},
		0, // TODO: tests for this?!
	}
	expectedRpcs := []mockSentRpc{
		{"s2", expectedRpcS2},
		{"s3", expectedRpcEmpty},
		{"s4", expectedRpcEmpty},
		{"s5", expectedRpcS5},
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

func TestCM_sendAppendEntriesToPeer(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine(t)
	serverTerm := mcm.pcm.persistentState.GetCurrentTerm()

	// sanity check
	expectedNextIndex := map[ServerId]LogIndex{"s2": 11, "s3": 11, "s4": 11, "s5": 11}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.nextIndex, expectedNextIndex) {
		t.Fatal()
	}

	// nothing to send
	mcm.pcm.sendAppendEntriesToPeer("s2", false)
	expectedRpc := &RpcAppendEntries{
		serverTerm,
		10,
		6,
		[]LogEntry{},
		0, // TODO: test this
	}
	expectedRpcs := []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// empty send
	mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	mcm.pcm.sendAppendEntriesToPeer("s2", true)
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{},
		0, // TODO: test this
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// send one
	mcm.pcm.sendAppendEntriesToPeer("s2", false)
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{
			{6, Command("c10")},
		},
		0,
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// send multiple
	mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	mcm.pcm.sendAppendEntriesToPeer("s2", false)
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		7,
		5,
		[]LogEntry{
			{6, Command("c8")},
			{6, Command("c9")},
			{6, Command("c10")},
		},
		0,
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

func TestCM_getEntriesAfterLogIndex(t *testing.T) {
	mcm, _ := testSetupMCM_Leader_Figure7LeaderLine(t)

	// none
	actualEntries := mcm.pcm.getEntriesAfterLogIndex(10)
	expectedEntries := []LogEntry{}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// one
	actualEntries = mcm.pcm.getEntriesAfterLogIndex(9)
	expectedEntries = []LogEntry{
		{6, Command("c10")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// multiple
	actualEntries = mcm.pcm.getEntriesAfterLogIndex(7)
	expectedEntries = []LogEntry{
		{6, Command("c8")},
		{6, Command("c9")},
		{6, Command("c10")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// max
	actualEntries = mcm.pcm.getEntriesAfterLogIndex(2)
	expectedEntries = []LogEntry{
		{1, Command("c3")},
		{4, Command("c4")},
		{4, Command("c5")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// index of 0
	actualEntries = mcm.pcm.getEntriesAfterLogIndex(0)
	expectedEntries = []LogEntry{
		{1, Command("c1")},
		{1, Command("c2")},
		{1, Command("c3")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// max alternate value
	mcm.pcm.maxEntriesPerAppendEntry = 2
	actualEntries = mcm.pcm.getEntriesAfterLogIndex(2)
	expectedEntries = []LogEntry{
		{1, Command("c3")},
		{4, Command("c4")},
	}
	if !reflect.DeepEqual(actualEntries, expectedEntries) {
		t.Fatal(actualEntries)
	}

	// index more than last log entry
	test_ExpectPanic(
		t,
		func() {
			mcm.pcm.getEntriesAfterLogIndex(11)
		},
		"indexOfLastEntry=11 is < afterLogIndex=10",
	)
}

// #RFS-L4: If there exists an N such that N > commitIndex, a majority
// of matchIndex[i] >= N, and log[N].term == currentTerm:
// set commitIndex = N (#5.3, #5.4)
// Note: test based on Figure 7; server is leader line; peers are other cases
func TestCM_Leader_TickAdvancesCommitIndexIfPossible(t *testing.T) {
	mcm, _ := testSetupMCM_Leader_Figure7LeaderLine(t)
	serverTerm := mcm.pcm.persistentState.GetCurrentTerm()

	// pre checks
	if serverTerm != 8 {
		t.Fatal()
	}
	if mcm.pcm.volatileState.commitIndex != 0 {
		t.Fatal()
	}

	// match peers for cases (a), (b), (c) & (d)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 9)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s3", 4)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s4", 10)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s5", 10)

	// tick should try to advance commitIndex but nothing should happen
	mcm.tick()
	if mcm.pcm.volatileState.commitIndex != 0 {
		t.Fatal()
	}

	// let's make a new log entry
	// TODO: drive this via a command?
	logEntries := []LogEntry{{serverTerm, Command("c11")}, {serverTerm, Command("c12")}}
	mcm.pcm.log.SetEntriesAfterIndex(10, logEntries)

	// tick should try to advance commitIndex but nothing should happen
	mcm.tick()
	if mcm.pcm.volatileState.commitIndex != 0 {
		t.Fatal()
	}

	// 2 peers - for cases (a) & (b) - catch up
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 11)
	mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s3", 11)

	// tick advances commitIndex
	mcm.tick()

	if mcm.pcm.volatileState.commitIndex != 11 {
		t.Fatal(mcm.pcm.volatileState.commitIndex)
	}
}

// #RFS-A1: If commitIndex > lastApplied: increment lastApplied, apply
// log[lastApplied] to state machine (#5.3)
// Note: test based on Figure 7; server is leader line
func TestCM_TickAdvancesCommitIndexIfPossible(t *testing.T) {
	f := func(setup func(t *testing.T) (mcm *managedConsensusModule, mrs *mockRpcSender)) {
		mcm, _ := setup(t)
		serverTerm := mcm.pcm.persistentState.GetCurrentTerm()

		// make a new log entry
		// TODO: drive this via a command?
		logEntries := []LogEntry{{serverTerm, Command("c11")}, {serverTerm, Command("c12")}}
		mcm.pcm.log.SetEntriesAfterIndex(10, logEntries)

		// pre checks
		if serverTerm != 8 {
			t.Fatal()
		}
		if mcm.pcm.volatileState.commitIndex != 0 {
			t.Fatal()
		}
		// TODO: log may have applied things!
		if mcm.pcm.log.GetLastApplied() != 0 {
			t.Fatal()
		}

		// tick should try to advance lastApplied but nothing should happen
		mcm.tick()
		if mcm.pcm.volatileState.commitIndex != 0 {
			t.Fatal()
		}
		// TODO: log may have applied things!
		if mcm.pcm.log.GetLastApplied() != 0 {
			t.Fatal()
		}

		// manually advance commitIndex a lot
		mcm.pcm.volatileState.commitIndex = 11

		// tick should advance lastApplied
		mcm.tick()
		if mcm.pcm.volatileState.commitIndex != 11 {
			t.Fatal()
		}
		// TODO: log may have applied things!
		if mcm.pcm.log.GetLastApplied() != 1 {
			t.Fatal()
		}

		// and again...
		mcm.tick()
		if mcm.pcm.volatileState.commitIndex != 11 {
			t.Fatal()
		}
		// TODO: log may have applied things!
		if mcm.pcm.log.GetLastApplied() != 2 {
			t.Fatal()
		}
	}

	f(testSetupMCM_FollowerTerm8_Figure7LeaderLine)
	f(testSetupMCM_Candidate_Figure7LeaderLine)
	f(testSetupMCM_Leader_Figure7LeaderLine)
}

func testSetupMCM_Follower_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := setupManagedConsensusModuleR2(t, terms)
	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}
	return mcm, mrs
}

func testSetupMCM_Candidate_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := testSetupMCM_Follower_WithTerms(t, terms)
	testCM_Follower_StartsElectionOnElectionTimeout(t, mcm, mrs)
	if mcm.pcm.getServerState() != CANDIDATE {
		t.Fatal()
	}
	return mcm, mrs
}

func testSetupMCM_Leader_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := testSetupMCM_Candidate_WithTerms(t, terms)
	serverTerm := mcm.pcm.persistentState.GetCurrentTerm()
	sentRpc := &RpcRequestVote{serverTerm, 0, 0}
	mcm.pcm.rpcReply("s2", sentRpc, &RpcRequestVoteReply{serverTerm, true})
	mcm.pcm.rpcReply("s3", sentRpc, &RpcRequestVoteReply{serverTerm, true})
	if mcm.pcm.getServerState() != LEADER {
		t.Fatal()
	}
	mrs.clearSentRpcs()
	return mcm, mrs
}

func testSetupMCM_Follower_Figure7LeaderLine(t *testing.T) (*managedConsensusModule, *mockRpcSender) {
	return testSetupMCM_Follower_WithTerms(t, makeLogTerms_Figure7LeaderLine())
}

func testSetupMCM_Candidate_Figure7LeaderLine(t *testing.T) (*managedConsensusModule, *mockRpcSender) {
	return testSetupMCM_Candidate_WithTerms(t, makeLogTerms_Figure7LeaderLine())
}

func testSetupMCM_Leader_Figure7LeaderLine(t *testing.T) (*managedConsensusModule, *mockRpcSender) {
	return testSetupMCM_Leader_WithTerms(t, makeLogTerms_Figure7LeaderLine())
}

func testSetupMCM_Leader_Figure7LeaderLine_WithUpToDatePeers(t *testing.T) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := testSetupMCM_Leader_WithTerms(t, makeLogTerms_Figure7LeaderLine())

	// sanity check - before
	expectedNextIndex := map[ServerId]LogIndex{"s2": 11, "s3": 11, "s4": 11, "s5": 11}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.nextIndex, expectedNextIndex) {
		t.Fatal()
	}
	expectedMatchIndex := map[ServerId]LogIndex{"s2": 0, "s3": 0, "s4": 0, "s5": 0}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.matchIndex, expectedMatchIndex) {
		t.Fatal()
	}

	// pretend peers caught up!
	lastLogIndex := mcm.pcm.log.GetIndexOfLastEntry()
	mcm.pcm.clusterInfo.ForEachPeer(
		func(serverId ServerId) {
			mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex(serverId, lastLogIndex)
		},
	)

	// after check
	expectedNextIndex = map[ServerId]LogIndex{"s2": 11, "s3": 11, "s4": 11, "s5": 11}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.nextIndex, expectedNextIndex) {
		t.Fatal()
	}
	expectedMatchIndex = map[ServerId]LogIndex{"s2": 10, "s3": 10, "s4": 10, "s5": 10}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.matchIndex, expectedMatchIndex) {
		t.Fatal()
	}

	return mcm, mrs
}

func TestCM_Follower_StartsElectionOnElectionTimeout_NonEmptyLog(t *testing.T) {
	testSetupMCM_Candidate_Figure7LeaderLine(t)
}

// For most tests, we'll use a passive CM where we control the progress
// of time with helper methods. This simplifies tests and avoids concurrency
// issues with inspecting the internals.
type managedConsensusModule struct {
	pcm *passiveConsensusModule
	now time.Time
}

func (mcm *managedConsensusModule) tick() {
	mcm.pcm.tick(mcm.now)
	mcm.now = mcm.now.Add(testTickerDuration)
}

func (mcm *managedConsensusModule) rpc(
	from ServerId,
	rpc interface{},
) interface{} {
	return mcm.pcm.rpc(from, rpc, mcm.now)
}

func (mcm *managedConsensusModule) tickTilElectionTimeout() {
	electionTimeoutTime := mcm.pcm.electionTimeoutTracker.electionTimeoutTime
	for {
		mcm.tick()
		if mcm.now.After(electionTimeoutTime) {
			break
		}
	}
	if mcm.pcm.electionTimeoutTracker.electionTimeoutTime != electionTimeoutTime {
		panic("electionTimeoutTime changed!")
	}
	// Because tick() increments "now" after calling tick(),
	// we need one more to actually run with a post-timeout "now".
	mcm.tick()
}
