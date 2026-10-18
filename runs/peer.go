package runs

import "strings"

// PeerLayer is the Delta.Source of every peer layer: a run a federated peer
// houston composed and published, kept apart from this host's own layers.
const PeerLayer = "peer"

const peerKeyPrefix = "peer/"

// PeerKey is the registry key of run id on peer host. Host names never contain
// "/" or "."; ids match ^[A-Za-z0-9_-]+$ (both validated by the peer package).
func PeerKey(host, id string) string { return peerKeyPrefix + host + "/" + id }

// peerHost returns the host of a PeerKey.
func peerHost(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, peerKeyPrefix)
	if !ok {
		return "", false
	}
	host, _, ok := strings.Cut(rest, "/")
	return host, ok
}

// peerAttention takes the attention the peer composed from layers this host
// does not have. needs-you is recomputed from the composed state (the only
// thing that means it), so a stray needs-you on a non-blocked run is dropped.
func peerAttention(out Run, peer Run) (Attention, string) {
	switch {
	case out.State == StateBlocked:
		return AttentionNeedsYou, ""
	case peer.Attention == AttentionNeedsYou:
		return AttentionNone, ""
	}
	return peer.Attention, peer.AttentionNote
}
