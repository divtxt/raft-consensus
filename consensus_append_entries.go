// AppendEntries RPC (Receiver Implementation)
// Invoked by leader to replicate log entries (#5.3); also used as heartbeat
// (#5.2).

package raft

func (cm *ConsensusModule) _processRpc_AppendEntries(appendEntries AppendEntries) (bool, error) {

	leaderCurrentTerm := appendEntries.term
	prevLogIndex := appendEntries.prevLogIndex

	// 1. Reply false if term < currentTerm (#5.1)
	if leaderCurrentTerm < cm.persistentState.currentTerm {
		return false, nil
	}

	// 2. Reply false if log doesn't contain an entry at prevLogIndex whose term
	//      matches prevLogTerm (#5.3)
	log := cm.persistentState.log
	if log.getIndexOfLastEntry() < prevLogIndex {
		return false, nil
	}

	// 3. If an existing entry conflicts with a new one (same index
	// but different terms), delete the existing entry and all that
	// follow it (#5.3)
	if log.getTermAtIndex(prevLogIndex) != appendEntries.prevLogTerm {
		log.deleteFromIndexToEnd(prevLogIndex)
		return false, nil
	}

	// 4. Append any new entries not already in the log
	err := log.appendEntriesAfterIndex(appendEntries.entries, prevLogIndex)
	if err != nil {
		return false, err
	}

	// 5. If leaderCommit > commitIndex, set commitIndex = min(leaderCommit,
	// index of last new entry)
	leaderCommit := appendEntries.leaderCommit
	if leaderCommit > cm.volatileState.commitIndex {
		indexOfLastNewEntry := log.getIndexOfLastEntry()
		if leaderCommit < indexOfLastNewEntry {
			cm.volatileState.commitIndex = leaderCommit
		} else {
			cm.volatileState.commitIndex = indexOfLastNewEntry
		}
	}

	return true, nil
}
