package rps_test

import (
	"bytes"
	"io/ioutil"
	"os"
	"testing"

	"github.com/divtxt/raft/fileutil"
	"github.com/divtxt/raft/rps"
	"github.com/divtxt/raft/testhelpers"
)

const (
	test_jsonfilerps = "test_jsonfilerps.json"
)

// Run the blackbox test on JsonFileRaftpersistentState
func TestNewJsonFileRaftpersistentState_Blackbox(t *testing.T) {
	ajf := fileutil.NewAtomicJsonFile(test_jsonfilerps)

	err := os.Remove(test_jsonfilerps)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	jfrps, err := rps.NewJsonFileRaftPersistentState(ajf)
	if err != nil {
		t.Fatal(err)
	}

	testhelpers.BlackboxTest_RaftPersistentState(t, jfrps)

	if jfrps.GetCurrentTerm() != 4 {
		t.Fatal()
	}
	if jfrps.GetVotedFor() != 2 {
		t.Fatal()
	}
}

// Run whitebox tests on JsonFileRaftpersistentState
func TestNewJsonFileRaftpersistentState_Whitebox(t *testing.T) {
	ajf := fileutil.NewAtomicJsonFile(test_jsonfilerps)

	err := os.Remove(test_jsonfilerps)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Non-existent file is initialization
	jfrps, err := rps.NewJsonFileRaftPersistentState(ajf)
	if err != nil {
		t.Fatal(err)
	}
	if jfrps.GetCurrentTerm() != 0 {
		t.Fatal(jfrps)
	}
	if jfrps.GetVotedFor() != 0 {
		t.Fatal()
	}
	// no file written for no changes
	data, err := ioutil.ReadFile(test_jsonfilerps)
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	// Set currentTerm and check file
	err = jfrps.SetCurrentTerm(1)
	if err != nil {
		t.Fatal(err)
	}

	data, err = ioutil.ReadFile(test_jsonfilerps)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(data, []byte("{\"currentTerm\":1,\"votedFor\":0}")) != 0 {
		t.Fatal(string(data))
	}

	// Set votedFor and check file
	err = jfrps.SetVotedFor(2000)
	if err != nil {
		t.Fatal(err)
	}

	data, err = ioutil.ReadFile(test_jsonfilerps)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(data, []byte("{\"currentTerm\":1,\"votedFor\":2000}")) != 0 {
		t.Fatal(string(data))
	}
}
