package webchat

import (
	_ "embed"
	"errors"
)

// ErrLiveUnavailable is returned when a live projection is requested. A live
// projection requires the supervisor-owned session/history/stream service, which
// does not exist yet. The gateway refuses rather than fabricating a live source.
var ErrLiveUnavailable = errors.New("live projection requires the supervisor-owned session/stream service; not implemented in this build")

//go:embed fixtures/demo-workspace.json
var demoFixture []byte

//go:embed fixtures/replay-gap.json
var replayGapFixture []byte

func newDocumentStore(raw []byte, source SourceClass, capacity int) (*Store, error) {
	snapshot, err := decodeDocument(raw, source)
	if err != nil {
		return nil, err
	}
	return NewStore(snapshot, capacity)
}

// NewFixtureStore builds a store from the embedded, non-claim-bearing
// development fixture.
func NewFixtureStore() (*Store, error) {
	return newDocumentStore(demoFixture, SourceFixture, DefaultItemCapacity)
}

// NewDemoReplayStore builds a store from the embedded retained-observation
// fixture, which demonstrates a continuity gap and an UNKNOWN outcome.
func NewDemoReplayStore() (*Store, error) {
	return newDocumentStore(replayGapFixture, SourceReplay, DefaultItemCapacity)
}

// NewFixtureStoreFromPath loads a development fixture from disk.
func NewFixtureStoreFromPath(path string) (*Store, error) {
	snapshot, err := LoadFixture(path)
	if err != nil {
		return nil, err
	}
	return NewStore(snapshot, DefaultItemCapacity)
}

// NewReplayStoreFromPath loads retained observations from disk.
func NewReplayStoreFromPath(path string) (*Store, error) {
	snapshot, err := LoadReplay(path)
	if err != nil {
		return nil, err
	}
	return NewStore(snapshot, DefaultItemCapacity)
}

// NewLiveStore refuses live projection until the authority-owned service exists.
func NewLiveStore() (*Store, error) {
	return nil, ErrLiveUnavailable
}
