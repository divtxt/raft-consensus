package raft

import (
	"errors"
	"fmt"
)

// A ClusterInfo holds the ServerIds of the servers in the Raft cluster and
// provides useful functions to work with this list.
type ClusterInfo struct {
	//
	thisServerId ServerId

	// Excludes thisServerId
	peerServerIds []ServerId

	//
	quorumSizeForCluster uint
}

// Allocate and initialize a NewClusterInfo with the given ServerIds.
//
//  - ServerIds must be distinct non-empty strings.
//  - allServerIds should list all the servers in the cluster.
//  - thisServerId is the ServerId of "this" server.
//  - allServerIds must include thisServerId.
//  - allServerIds must contain at least 2 elements.
//
func NewClusterInfo(
	allServerIds []ServerId,
	thisServerId ServerId,
) (*ClusterInfo, error) {
	if allServerIds == nil {
		return nil, errors.New("allServerIds is nil")
	}
	if len(allServerIds) < 2 {
		return nil, errors.New("allServerIds must have at least 2 elements")
	}
	if len(thisServerId) == 0 {
		return nil, errors.New("thisServerId is empty string")
	}

	allServerIdsMap := make(map[ServerId]bool)
	clusterSize := len(allServerIds)
	peerServerIds := make([]ServerId, 0, clusterSize-1)
	for _, serverId := range allServerIds {
		if len(serverId) == 0 {
			return nil, errors.New("allServerIds contains empty string")
		}
		if _, ok := allServerIdsMap[serverId]; ok {
			return nil, fmt.Errorf("allServerIds contains duplicate value: %v", serverId)
		}
		allServerIdsMap[serverId] = true
		if serverId != thisServerId {
			peerServerIds = append(peerServerIds, serverId)
		}
	}

	if _, ok := allServerIdsMap[thisServerId]; !ok {
		return nil, fmt.Errorf("allServerIds does not contain thisServerId: %v", thisServerId)
	}

	quorumSizeForCluster := QuorumSizeForClusterSize((uint)(clusterSize))

	ci := &ClusterInfo{
		thisServerId,
		peerServerIds,
		quorumSizeForCluster,
	}

	return ci, nil
}

// Get the ServerId of "this" server.
func (ci *ClusterInfo) GetThisServerId() ServerId {
	return ci.thisServerId
}

// Iterate over the list of all peer servers in the cluster and call the given
// function with it's ServerId.
//
// "Peer" servers here means all servers except for "this" server.
func (ci *ClusterInfo) ForEachPeer(f func(serverId ServerId)) {
	for _, serverId := range ci.peerServerIds {
		f(serverId)
	}
}

// Get the quorum size for this ClusterInfo.
//
// Same as QuorumSizeForClusterSize with the cluster size of this ClusterInfo.
func (ci *ClusterInfo) QuorumSizeForCluster() uint {
	return ci.quorumSizeForCluster
}

// Get the quorum size for a cluster of given size.
func QuorumSizeForClusterSize(clusterSize uint) uint {
	return (clusterSize / 2) + 1
}
