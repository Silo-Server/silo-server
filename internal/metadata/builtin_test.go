package metadata

type builtinStubProvider struct{ slug string }

func (p *builtinStubProvider) Slug() string       { return p.slug }
func (p *builtinStubProvider) Name() string       { return p.slug }
func (p *builtinStubProvider) ForTypes() []string { return []string{"movie", "series"} }
