package raft

import (
	"reflect"
	"testing"
	"time"
)

const (
	testThisServerId = "s1"

	// Note: value for tests based on Figure 7
	// Start as follower at term 7 so that leader will be at term 8
	testCurrentTerm = 7

	testTickerDuration     = 30 * time.Millisecond
	testElectionTimeoutLow = 150 * time.Millisecond

	testSleepToLetGoroutineRun = 3 * time.Millisecond
	testSleepJustMoreThanATick = testTickerDuration + testSleepToLetGoroutineRun

	testMaxEntriesPerAppendEntry = 3
)

var testAllServerIds = []ServerId{testThisServerId, "s2", "s3", "s4", "s5"}

func setupManagedConsensusModule(t *testing.T, logTerms []TermNo) *managedConsensusModule {
	mcm, _ := setupManagedConsensusModuleR2(t, logTerms, false)
	return mcm
}

func setupManagedConsensusModuleR2(
	t *testing.T,
	logTerms []TermNo,
	solo bool,
) (*managedConsensusModule, *mockRpcSender) {
	ps := newIMPSWithCurrentTerm(testCurrentTerm)
	imle := newIMLEWithDummyCommands(logTerms, testMaxEntriesPerAppendEntry)
	mrs := newMockRpcSender()
	var allServerIds []ServerId
	if solo {
		allServerIds = []ServerId{testThisServerId}
	} else {
		allServerIds = testAllServerIds
	}
	ci, err := NewClusterInfo(allServerIds, testThisServerId)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cm, err := newPassiveConsensusModule(
		ps,
		imle,
		mrs,
		ci,
		testElectionTimeoutLow,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cm == nil {
		t.Fatal()
	}
	// Bias simulated clock to avoid exact time matches
	now = now.Add(testSleepToLetGoroutineRun)
	mcm := &managedConsensusModule{cm, now, imle}
	return mcm, mrs
}

// #5.2-p1s2: When servers start up, they begin as followers
func TestCM_InitialState(t *testing.T) {
	mcm := setupManagedConsensusModule(t, nil)

	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}

	// Volatile state on all servers
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
}

func TestCM_SetServerState_BadServerStateReturnsError(t *testing.T) {
	mcm := setupManagedConsensusModule(t, nil)
	err := mcm.pcm.setServerState(42)
	if err.Error() != "FATAL: unknown ServerState: 42" {
		t.Fatal(err)
	}
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
	if mcm.pcm.raftPersistentState.GetVotedFor() != "" {
		t.Fatal()
	}

	// Test that a tick before election timeout causes no state change.
	err := mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.raftPersistentState.GetCurrentTerm() != testCurrentTerm {
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
	mcm.tickTilElectionTimeout(t)
	if mcm.pcm.raftPersistentState.GetCurrentTerm() != expectedNewTerm {
		t.Fatal(expectedNewTerm, mcm.pcm.raftPersistentState.GetCurrentTerm())
	}
	if mcm.pcm.getServerState() != CANDIDATE {
		t.Fatal()
	}
	// candidate has voted for itself
	if mcm.pcm.raftPersistentState.GetVotedFor() != testThisServerId {
		t.Fatal()
	}
	// a new election timeout was chosen
	// Playing the odds here :P
	if mcm.pcm.electionTimeoutTracker.currentElectionTimeout == timeout1 {
		t.Fatal()
	}
	// candidate state is fresh
	expectedCvs, err := newCandidateVolatileState(mcm.pcm.clusterInfo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mcm.pcm.candidateVolatileState, expectedCvs) {
		t.Fatal()
	}

	// candidate has issued RequestVote RPCs to all other servers.
	lastLogIndex, lastLogTerm, err := GetIndexAndTermOfLastEntry(mcm.pcm.lasm)
	if err != nil {
		t.Fatal(err)
	}
	expectedRpc := &RpcRequestVote{expectedNewTerm, lastLogIndex, lastLogTerm}
	expectedRpcs := make([]mockSentRpc, 0, 4)
	err = mcm.pcm.clusterInfo.ForEachPeer(func(serverId ServerId) error {
		expectedRpcs = append(expectedRpcs, mockSentRpc{serverId, expectedRpc})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

func testCM_SOLO_Follower_ElectsSelfOnElectionTimeout(
	t *testing.T,
	mcm *managedConsensusModule,
	mrs *mockRpcSender,
) {
	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}
	if mcm.pcm.raftPersistentState.GetVotedFor() != "" {
		t.Fatal()
	}

	// Test that a tick before election timeout causes no state change.
	err := mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.raftPersistentState.GetCurrentTerm() != testCurrentTerm {
		t.Fatal()
	}
	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}

	var expectedNewTerm TermNo = testCurrentTerm + 1

	timeout1 := mcm.pcm.electionTimeoutTracker.currentElectionTimeout
	// Test that election timeout causes a new election
	mcm.tickTilElectionTimeout(t)
	if mcm.pcm.raftPersistentState.GetCurrentTerm() != expectedNewTerm {
		t.Fatal(expectedNewTerm, mcm.pcm.raftPersistentState.GetCurrentTerm())
	}
	// Single node should immediately elect itself as leader
	if mcm.pcm.getServerState() != LEADER {
		t.Fatal()
	}
	// candidate has voted for itself
	if mcm.pcm.raftPersistentState.GetVotedFor() != testThisServerId {
		t.Fatal()
	}
	// a new election timeout was chosen
	if mcm.pcm.electionTimeoutTracker.currentElectionTimeout == timeout1 {
		t.Fatal()
	}
	// candidate state is fresh
	expectedCvs, err := newCandidateVolatileState(mcm.pcm.clusterInfo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mcm.pcm.candidateVolatileState, expectedCvs) {
		t.Fatal()
	}

	// no RPCs issued
	mrs.checkSentRpcs(t, []mockSentRpc{})
}

func TestCM_Follower_StartsElectionOnElectionTimeout_EmptyLog(t *testing.T) {
	mcm, mrs := setupManagedConsensusModuleR2(t, nil, false)
	testCM_Follower_StartsElectionOnElectionTimeout(t, mcm, mrs)
}

func TestCM_SOLO_Follower_ElectsSelfOnElectionTimeout_EmptyLog(t *testing.T) {
	mcm, mrs := setupManagedConsensusModuleR2(t, nil, true)
	testCM_SOLO_Follower_ElectsSelfOnElectionTimeout(t, mcm, mrs)
}

// #RFS-L1b: repeat during idle periods to prevent election timeout (#5.2)
func TestCM_Leader_SendEmptyAppendEntriesDuringIdlePeriods(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine(t)
	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()
	err := mcm.pcm.setCommitIndex(6)
	if err != nil {
		t.Fatal(err)
	}

	mrs.checkSentRpcs(t, []mockSentRpc{})

	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}

	testIsLeaderWithTermAndSentEmptyAppendEntries(t, mcm, mrs, serverTerm)
}

// #RFS-L3.0: If last log index >= nextIndex for a follower: send
// AppendEntries RPC with log entries starting at nextIndex
func TestCM_Leader_TickSendsAppendEntriesWithLogEntries(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine_WithUpToDatePeers(t)
	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()
	err := mcm.pcm.setCommitIndex(5)
	if err != nil {
		t.Fatal(err)
	}

	// repatch some peers as not caught up
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 9)
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s5", 7)
	if err != nil {
		t.Fatal(err)
	}

	// tick should trigger check & appropriate sends
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}

	expectedRpcEmpty := &RpcAppendEntries{
		serverTerm,
		10,
		6,
		[]LogEntry{},
		5,
	}
	expectedRpcS2 := &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{
			{6, Command("c10")},
		},
		5,
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
		5,
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
	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()
	err := mcm.pcm.setCommitIndex(4)
	if err != nil {
		t.Fatal(err)
	}

	// sanity check
	expectedNextIndex := map[ServerId]LogIndex{"s2": 11, "s3": 11, "s4": 11, "s5": 11}
	if !reflect.DeepEqual(mcm.pcm.leaderVolatileState.nextIndex, expectedNextIndex) {
		t.Fatal()
	}

	// nothing to send
	err = mcm.pcm.sendAppendEntriesToPeer("s2", false)
	if err != nil {
		t.Fatal(err)
	}
	expectedRpc := &RpcAppendEntries{
		serverTerm,
		10,
		6,
		[]LogEntry{},
		4,
	}
	expectedRpcs := []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// empty send
	err = mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.sendAppendEntriesToPeer("s2", true)
	if err != nil {
		t.Fatal(err)
	}
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{},
		4,
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// send one
	err = mcm.pcm.sendAppendEntriesToPeer("s2", false)
	if err != nil {
		t.Fatal(err)
	}
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		9,
		6,
		[]LogEntry{
			{6, Command("c10")},
		},
		4,
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// send multiple
	err = mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.decrementNextIndex("s2")
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.sendAppendEntriesToPeer("s2", false)
	if err != nil {
		t.Fatal(err)
	}
	expectedRpc = &RpcAppendEntries{
		serverTerm,
		7,
		5,
		[]LogEntry{
			{6, Command("c8")},
			{6, Command("c9")},
			{6, Command("c10")},
		},
		4,
	}
	expectedRpcs = []mockSentRpc{
		{"s2", expectedRpc},
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

/*
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
*/

// #RFS-L4: If there exists an N such that N > commitIndex, a majority
// of matchIndex[i] >= N, and log[N].term == currentTerm:
// set commitIndex = N (#5.3, #5.4)
// Note: test based on Figure 7; server is leader line; peers are other cases
func TestCM_Leader_TickAdvancesCommitIndexIfPossible(t *testing.T) {
	mcm, mrs := testSetupMCM_Leader_Figure7LeaderLine(t)
	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()

	// pre checks
	if serverTerm != 8 {
		t.Fatal()
	}
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
	expectedRpcs := []mockSentRpc{}
	mrs.checkSentRpcs(t, expectedRpcs)

	// match peers for cases (a), (b), (c) & (d)
	err := mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 9)
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s3", 4)
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s4", 10)
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s5", 10)
	if err != nil {
		t.Fatal(err)
	}

	// tick should try to advance commitIndex but nothing should happen
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
	expectedRpcs = []mockSentRpc{
		{
			"s2",
			&RpcAppendEntries{serverTerm, 9, 6, []LogEntry{
				{6, Command("c10")},
			}, 0},
		},
		{
			"s3",
			&RpcAppendEntries{serverTerm, 4, 4, []LogEntry{
				{4, Command("c5")},
				{5, Command("c6")},
				{5, Command("c7")},
			}, 0},
		},
		{
			"s4",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{}, 0},
		},
		{
			"s5",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{}, 0},
		},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// let's make some new log entries
	result, err := mcm.pcm.appendCommand("c11")
	if result != inMemoryLog_AppendEntry_Ok || err != nil {
		t.Fatal()
	}
	result, err = mcm.pcm.appendCommand("c12")
	if result != inMemoryLog_AppendEntry_Ok || err != nil {
		t.Fatal()
	}

	// tick should try to advance commitIndex but nothing should happen
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
	expectedRpcs = []mockSentRpc{
		{
			"s2",
			&RpcAppendEntries{serverTerm, 9, 6, []LogEntry{
				{6, Command("c10")},
				{8, Command("c11")},
				{8, Command("c12")},
			}, 0},
		},
		{
			"s3",
			&RpcAppendEntries{serverTerm, 4, 4, []LogEntry{
				{4, Command("c5")},
				{5, Command("c6")},
				{5, Command("c7")},
			}, 0},
		},
		{
			"s4",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{
				{8, Command("c11")},
				{8, Command("c12")},
			}, 0},
		},
		{
			"s5",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{
				{8, Command("c11")},
				{8, Command("c12")},
			}, 0},
		},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// 2 peers - for cases (a) & (b) - catch up
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s2", 11)
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex("s3", 11)
	if err != nil {
		t.Fatal(err)
	}

	// tick advances commitIndex
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 11 {
		t.Fatal()
	}
	expectedRpcs = []mockSentRpc{
		{
			"s2",
			&RpcAppendEntries{serverTerm, 11, 8, []LogEntry{
				{8, Command("c12")},
			}, 11},
		},
		{
			"s3",
			&RpcAppendEntries{serverTerm, 11, 8, []LogEntry{
				{8, Command("c12")},
			}, 11},
		},
		{
			"s4",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{
				{8, Command("c11")},
				{8, Command("c12")},
			}, 11},
		},
		{
			"s5",
			&RpcAppendEntries{serverTerm, 10, 6, []LogEntry{
				{8, Command("c11")},
				{8, Command("c12")},
			}, 11},
		},
	}
	mrs.checkSentRpcs(t, expectedRpcs)

	// replies never came back -> tick cannot advance commitIndex
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 11 {
		t.Fatal(mcm.pcm.getCommitIndex())
	}
	mrs.checkSentRpcs(t, expectedRpcs)
}

func TestCM_SOLO_Leader_TickAdvancesCommitIndexIfPossible(t *testing.T) {
	var err error
	mcm, mrs := testSetupMCM_SOLO_Leader_WithTerms(t, makeLogTerms_Figure7LeaderLine())

	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()

	// pre checks
	if serverTerm != 8 {
		t.Fatal()
	}
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
	mrs.checkSentRpcs(t, []mockSentRpc{})

	// tick should try to advance commitIndex but nothing should happen
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}
	mrs.checkSentRpcs(t, []mockSentRpc{})

	// let's make some new log entries
	result, err := mcm.pcm.appendCommand("c11")
	if result != inMemoryLog_AppendEntry_Ok || err != nil {
		t.Fatal()
	}
	result, err = mcm.pcm.appendCommand("c12")
	if result != inMemoryLog_AppendEntry_Ok || err != nil {
		t.Fatal()
	}

	// commitIndex does not advance immediately
	if mcm.pcm.getCommitIndex() != 0 {
		t.Fatal()
	}

	// tick will advance commitIndex to the highest match possible
	err = mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getCommitIndex() != 12 {
		t.Fatal(mcm.pcm.getCommitIndex())
	}
	mrs.checkSentRpcs(t, []mockSentRpc{})
}

func TestCM_SetCommitIndexNotifiesLog(t *testing.T) {
	f := func(setup func(t *testing.T) (mcm *managedConsensusModule, mrs *mockRpcSender)) {
		mcm, _ := setup(t)

		if mcm.iml._commitIndex != 0 {
			t.Fatal()
		}

		err := mcm.pcm.setCommitIndex(2)
		if err != nil {
			t.Fatal(err)
		}
		if mcm.iml._commitIndex != 2 {
			t.Fatal()
		}

		err = mcm.pcm.setCommitIndex(9)
		if err != nil {
			t.Fatal(err)
		}
		if mcm.iml._commitIndex != 9 {
			t.Fatal()
		}
	}

	f(testSetupMCM_Follower_Figure7LeaderLine)
	f(testSetupMCM_Candidate_Figure7LeaderLine)
	f(testSetupMCM_Leader_Figure7LeaderLine)
}

func testSetupMCM_Follower_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := setupManagedConsensusModuleR2(t, terms, false)
	if mcm.pcm.getServerState() != FOLLOWER {
		t.Fatal()
	}
	return mcm, mrs
}

func testSetupMCM_SOLO_Follower_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := setupManagedConsensusModuleR2(t, terms, true)
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
	serverTerm := mcm.pcm.raftPersistentState.GetCurrentTerm()
	sentRpc := &RpcRequestVote{serverTerm, 0, 0}
	err := mcm.pcm.rpcReply_RpcRequestVoteReply("s2", sentRpc, &RpcRequestVoteReply{serverTerm, true})
	if err != nil {
		t.Fatal(err)
	}
	err = mcm.pcm.rpcReply_RpcRequestVoteReply("s3", sentRpc, &RpcRequestVoteReply{serverTerm, true})
	if err != nil {
		t.Fatal(err)
	}
	if mcm.pcm.getServerState() != LEADER {
		t.Fatal()
	}
	mrs.clearSentRpcs()
	return mcm, mrs
}

func testSetupMCM_SOLO_Leader_WithTerms(
	t *testing.T,
	terms []TermNo,
) (*managedConsensusModule, *mockRpcSender) {
	mcm, mrs := testSetupMCM_SOLO_Follower_WithTerms(t, terms)
	testCM_SOLO_Follower_ElectsSelfOnElectionTimeout(t, mcm, mrs)
	if mcm.pcm.getServerState() != LEADER {
		t.Fatal()
	}
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
	lastLogIndex, err := mcm.pcm.lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	err = mcm.pcm.clusterInfo.ForEachPeer(
		func(serverId ServerId) error {
			err := mcm.pcm.leaderVolatileState.setMatchIndexAndNextIndex(serverId, lastLogIndex)
			if err != nil {
				return err
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal()
	}

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

// #RFS-L2a: If command received from client: append entry to local log
func TestCM_Leader_AppendCommand(t *testing.T) {
	mcm, _ := testSetupMCM_Leader_Figure7LeaderLine(t)

	// pre check
	iole, err := mcm.pcm.lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 10 {
		t.Fatal()
	}

	result, err := mcm.pcm.appendCommand("c11x")

	if result != inMemoryLog_AppendEntry_Ok || err != nil {
		t.Fatal()
	}
	iole, err = mcm.pcm.lasm.GetIndexOfLastEntry()
	if err != nil {
		t.Fatal()
	}
	if iole != 11 {
		t.Fatal()
	}
	le := testHelper_GetLogEntryAtIndex(mcm.pcm.lasm, 11)
	if !reflect.DeepEqual(le, LogEntry{8, Command("c11x")}) {
		t.Fatal(le)
	}
}

// #RFS-L2a: If command received from client: append entry to local log
func TestCM_FollowerOrCandidate_AppendCommand(t *testing.T) {
	f := func(
		setup func(t *testing.T) (mcm *managedConsensusModule, mrs *mockRpcSender),
	) {
		mcm, _ := setup(t)

		// pre check
		iole, err := mcm.pcm.lasm.GetIndexOfLastEntry()
		if err != nil {
			t.Fatal()
		}
		if iole != 10 {
			t.Fatal()
		}

		result, err := mcm.pcm.appendCommand("c11x")
		if err != nil {
			t.Fatal(err)
		}
		if result != nil {
			t.Fatal(result)
		}

		iole, err = mcm.pcm.lasm.GetIndexOfLastEntry()
		if err != nil {
			t.Fatal()
		}
		if iole != 10 {
			t.Fatal()
		}
	}

	f(testSetupMCM_Follower_Figure7LeaderLine)
	f(testSetupMCM_Candidate_Figure7LeaderLine)
}

// For most tests, we'll use a passive CM where we control the progress
// of time with helper methods. This simplifies tests and avoids concurrency
// issues with inspecting the internals.
type managedConsensusModule struct {
	pcm *passiveConsensusModule
	now time.Time
	iml *inMemoryLog
}

func (mcm *managedConsensusModule) tick() error {
	err := mcm.pcm.tick(mcm.now)
	if err != nil {
		return err
	}
	mcm.now = mcm.now.Add(testTickerDuration)
	return nil
}

func (mcm *managedConsensusModule) rpc_RpcAppendEntries(
	from ServerId,
	rpc *RpcAppendEntries,
) (*RpcAppendEntriesReply, error) {
	return mcm.pcm.rpc_RpcAppendEntries(from, rpc, mcm.now)
}

func (mcm *managedConsensusModule) rpc_RpcRequestVote(
	from ServerId,
	rpc *RpcRequestVote,
) (*RpcRequestVoteReply, error) {
	return mcm.pcm.rpc_RpcRequestVote(from, rpc, mcm.now)
}

func (mcm *managedConsensusModule) tickTilElectionTimeout(t *testing.T) {
	electionTimeoutTime := mcm.pcm.electionTimeoutTracker.electionTimeoutTime
	for {
		err := mcm.tick()
		if err != nil {
			t.Fatal(err)
		}
		if mcm.now.After(electionTimeoutTime) {
			break
		}
	}
	if mcm.pcm.electionTimeoutTracker.electionTimeoutTime != electionTimeoutTime {
		t.Fatal("electionTimeoutTime changed!")
	}
	// Because tick() increments "now" after calling tick(),
	// we need one more to actually run with a post-timeout "now".
	err := mcm.tick()
	if err != nil {
		t.Fatal(err)
	}
}
