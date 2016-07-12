package raft_extra

import (
	"bytes"
	"github.com/divtxt/raft"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

// Run the blackbox test on JsonFileRaftpersistentState
func TestNewJsonFileRaftpersistentState_Blackbox(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(wd, "test_jsonfilerps.json")

	err = os.Remove(filename)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	jfrps, err := NewJsonFileRaftPersistentState(filename)
	if err != nil {
		t.Fatal(err)
	}

	raft.BlackboxTest_RaftPersistentState(t, jfrps)

	if jfrps.GetCurrentTerm() != 4 {
		t.Fatal()
	}
	if jfrps.GetVotedFor() != "s2" {
		t.Fatal()
	}
}

// Run whitebox tests on JsonFileRaftpersistentState
func TestNewJsonFileRaftpersistentState_Whitebox(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(wd, "test_jsonfilerps.json")

	err = os.Remove(filename)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Non-existent file is initialization
	jfrps, err := NewJsonFileRaftPersistentState(filename)
	if err != nil {
		t.Fatal(err)
	}
	if jfrps.GetCurrentTerm() != 0 {
		t.Fatal(jfrps)
	}
	if jfrps.GetVotedFor() != "" {
		t.Fatal()
	}
	// no file written for no changes
	data, err := ioutil.ReadFile(filename)
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Set currentTerm and check file
	err = jfrps.SetCurrentTerm(1)
	if err != nil {
		t.Fatal(err)
	}

	data, err = ioutil.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(data, []byte("{\"currentTerm\":1,\"votedFor\":\"\"}")) != 0 {
		t.Fatal(string(data))
	}

	// Set votedFor and check file
	err = jfrps.SetVotedFor("s2000")
	if err != nil {
		t.Fatal(err)
	}

	data, err = ioutil.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(data, []byte("{\"currentTerm\":1,\"votedFor\":\"s2000\"}")) != 0 {
		t.Fatal(string(data))
	}
}