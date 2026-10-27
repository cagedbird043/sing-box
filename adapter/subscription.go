package adapter

import (
	"github.com/sagernet/sing-box/common/hash"
	E "github.com/sagernet/sing/common/exceptions"
	"time"
)

// SavedSubscription deliberately has its own version and bucket. Rule-set v2
// and the old fork's subscription v2 have different wire layouts.
type SavedSubscription struct {
	Hash        hash.HashType
	Content     []byte
	LastUpdated time.Time
	LastEtag    string
	URLHash     []byte
}

func (s *SavedSubscription) MarshalBinary() ([]byte, error) {
	payload, err := (&SavedBinary{Content: s.Content, LastUpdated: s.LastUpdated, LastEtag: s.LastEtag, URLHash: s.URLHash}).MarshalBinary()
	if err != nil {
		return nil, err
	}
	data := make([]byte, 1+s.Hash.Len()+len(payload))
	data[0] = 3
	copy(data[1:], s.Hash.Bytes())
	copy(data[1+s.Hash.Len():], payload)
	return data, nil
}

func (s *SavedSubscription) UnmarshalBinary(data []byte) error {
	if len(data) < 1+s.Hash.Len() || data[0] != 3 {
		return E.New("invalid subscription cache version")
	}
	if err := s.Hash.UnmarshalBinary(data[1 : 1+s.Hash.Len()]); err != nil {
		return err
	}
	var payload SavedBinary
	if err := payload.UnmarshalBinary(data[1+s.Hash.Len():]); err != nil {
		return err
	}
	s.Content, s.LastUpdated, s.LastEtag, s.URLHash = payload.Content, payload.LastUpdated, payload.LastEtag, payload.URLHash
	return nil
}
