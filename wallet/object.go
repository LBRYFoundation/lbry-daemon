package wallet

// Member is one insertion-ordered JSON object member.
type Member struct {
	Key   string
	Value any
}

// Object preserves Python dict insertion order while retaining keyed access.
// It is not safe for concurrent mutation.
type Object struct {
	members []Member
	index   map[string]int
}

func NewObject(members ...Member) *Object {
	object := &Object{index: make(map[string]int, len(members))}
	for _, member := range members {
		object.Set(member.Key, member.Value)
	}
	return object
}

func (object *Object) Get(key string) (any, bool) {
	if object == nil {
		return nil, false
	}
	index, exists := object.index[key]
	if !exists {
		return nil, false
	}
	return object.members[index].Value, true
}

// Set replaces an existing value without changing its insertion position.
func (object *Object) Set(key string, value any) {
	if object.index == nil {
		object.index = make(map[string]int)
	}
	if index, exists := object.index[key]; exists {
		object.members[index].Value = value
		return
	}
	object.index[key] = len(object.members)
	object.members = append(object.members, Member{Key: key, Value: value})
}

func (object *Object) Delete(key string) bool {
	if object == nil {
		return false
	}
	index, exists := object.index[key]
	if !exists {
		return false
	}
	copy(object.members[index:], object.members[index+1:])
	object.members = object.members[:len(object.members)-1]
	delete(object.index, key)
	for position := index; position < len(object.members); position++ {
		object.index[object.members[position].Key] = position
	}
	return true
}

func (object *Object) Keys() []string {
	if object == nil {
		return []string{}
	}
	keys := make([]string, len(object.members))
	for index, member := range object.members {
		keys[index] = member.Key
	}
	return keys
}

func (object *Object) Members() []Member {
	if object == nil {
		return []Member{}
	}
	return append([]Member(nil), object.members...)
}

func (object *Object) Len() int {
	if object == nil {
		return 0
	}
	return len(object.members)
}

func (object *Object) ShallowCopy() *Object {
	if object == nil {
		return NewObject()
	}
	return NewObject(object.members...)
}

func (object *Object) MarshalJSON() ([]byte, error) {
	return encodePreferenceJSON(object)
}

func (object *Object) hasSameKeys(other *Object) bool {
	if object.Len() != other.Len() {
		return false
	}
	for _, member := range object.members {
		if _, exists := other.Get(member.Key); !exists {
			return false
		}
	}
	return true
}
