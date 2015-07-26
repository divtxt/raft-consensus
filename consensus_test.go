package raft

import (
	"testing"
)

// #5.2-p1s2: When servers start up, they begin as followers
func TestCMStartsAsFollower(t *testing.T) {
	cm := NewConsensusModule(PersistentState{TEST_CURRENT_TERM, nil})

	if cm == nil {
		t.Fatal()
	}
	if cm.serverMode != FOLLOWER {
		t.Fatal()
	}
}