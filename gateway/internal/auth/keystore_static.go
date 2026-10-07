package auth

type StaticKeyStore struct {
	data map[string]*Identity
}

func NewStaticKeyStore(data map[string]*Identity) *StaticKeyStore {
	return &StaticKeyStore{
		data: data,
	}
}

func (s *StaticKeyStore) Get(key string) (*Identity, error) {
	value, ok := s.data[key]
	if !ok {
		return nil, ErrorKeyNotFound
	}

	return value, nil
}
