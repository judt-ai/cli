package cli

//go:generate go run ../gen -spec ../../spec/openapi.json -commands ../../commands.yaml -out operations_gen.go

// Section is a heading that a group's --help sorts its subcommands under.
type Section struct{ ID, Title string }

// Group is a command that holds other commands, such as `judt apps`.
type Group struct {
	Path, Section, Short, Long string
	Aliases, Example           []string
	Sections                   []Section
}

// Param is a query parameter or a body field. A body field inside an object field
// (production.visibility) has the object's name as Parent.
type Param struct {
	Name, Parent, Description string
	// Type is string, integer, number, boolean, array (comma-separated) or json.
	Type     string
	Enum     []string
	Required bool
}

// Operation is one API operation and the command that runs it.
type Operation struct {
	ID, Command, Method, Path, Summary, Description string
	Section, Shortcut                               string
	Aliases, Example                                []string
	// Args are positional: path parameters or top-level body fields, in order.
	Args []string
	// AppArg lets the first positional argument name the App instead of --app.
	AppArg bool
	// AppScoped operations act on one App, the path's {id}.
	AppScoped bool
	HasBody   bool
	// Keyed operations take an Idempotency-Key.
	Keyed  bool
	Secret []string
	// Print is a string field printed as-is in a terminal; Exit an integer field
	// that becomes the exit code.
	Print, Exit string
	Confirm     bool
	Query, Body []Param
}
