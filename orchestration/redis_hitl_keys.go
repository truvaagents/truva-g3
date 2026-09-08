package orchestration

type hitlKeys struct {
	base string
}

func newHITLKeys(base string) hitlKeys { return hitlKeys{base: base} }

func (keys hitlKeys) checkpoint(id string) string { return keys.base + ":checkpoint:" + id }
func (keys hitlKeys) pending() string             { return keys.base + ":pending" }
func (keys hitlKeys) request(id string) string    { return keys.base + ":request:" + id }
func (keys hitlKeys) claim(id string) string      { return keys.base + ":claim:" + id }
func (keys hitlKeys) command(id string) string    { return keys.base + ":command:" + id }
