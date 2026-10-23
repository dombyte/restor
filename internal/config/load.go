package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v4"
)

var (
	// ErrInvalid is wrapped by every validation problem (see FieldError).
	ErrInvalid = errors.New("invalid")
	// ErrFormat is returned for a configuration file that is not YAML.
	ErrFormat = errors.New("unsupported format, use a .yaml or .yml file")
	// ErrEmpty is returned for a configuration file without content.
	ErrEmpty = errors.New("empty configuration file")
)

// FieldError is one validation problem, with the path of the field.
type FieldError struct {
	// Path is the dotted field path, e.g. "jobs.web.compose_file".
	Path string
	// Problem says what is wrong.
	Problem string
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Problem }

// Unwrap returns ErrInvalid.
func (e *FieldError) Unwrap() error { return ErrInvalid }

// Load reads the file at path and returns the validated Plan. ${VAR} references in values
// are expanded with getenv, relative paths are resolved against the file's directory and
// the repository's env file is read. All problems are returned together (errors.Join).
func Load(path string, getenv func(string) string) (*Plan, error) {
	root, err := readFile(path)
	if err != nil {
		return nil, err
	}
	keyErrs := unknownKeys(root, reflect.TypeFor[File](), "")
	expandNode(root, getenv)
	var f File
	if err := root.Decode(&f); err != nil {
		return nil, fmt.Errorf("config: %w", errors.Join(append(keyErrs, decodeErrors(err)...)...))
	}
	keyErrs = append(keyErrs, f.homePaths()...)
	if err := f.resolvePaths(path); err != nil {
		return nil, err
	}
	plan, errs := f.plan()
	if err := errors.Join(append(keyErrs, errs...)...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return plan, nil
}

// readFile parses the YAML file at path and returns its top-level node.
func readFile(path string) (*yaml.Node, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("config: %s: %w", path, ErrFormat)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("config: %s: %w", path, ErrEmpty)
	}
	return doc.Content[0], nil
}

// decodeErrors splits a YAML decoding error into one error per problem.
func decodeErrors(err error) []error {
	var load *yaml.LoadErrors
	if !errors.As(err, &load) {
		return []error{err}
	}
	errs := make([]error, 0, len(load.Errors))
	for _, e := range load.Errors {
		errs = append(errs, &FieldError{
			Path: fmt.Sprintf("line %d", e.Mark.Line), Problem: e.Message,
		})
	}
	return errs
}

// unknownKeys returns an error for every mapping key below n that matches no field of t,
// with a hint for the keys of format version 1. Type mismatches are left to Decode.
// Aliases are followed and merge keys (<<) checked against t; top-level keys starting
// with "x-" are free, as a place for anchors.
func unknownKeys(n *yaml.Node, t reflect.Type, path string) []error {
	n = resolveAlias(n)
	childType := fieldTypes(t)
	if n.Kind != yaml.MappingNode || childType == nil {
		return nil
	}
	var errs []error
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, child := n.Content[i], joinPath(path, n.Content[i].Value)
		if skip, merged := special(key.Value, path); skip {
			if merged {
				errs = append(errs, mergedKeys(n.Content[i+1], t, path)...)
			}
			continue
		}
		elem, ok := childType(key.Value)
		if !ok {
			errs = append(errs, &FieldError{Path: child, Problem: unknownProblem(key)})
			continue
		}
		errs = append(errs, unknownKeys(n.Content[i+1], elem, child)...)
	}
	return errs
}

// fieldTypes returns the type of a key of a mapping decoded into t, or nil when t is no
// struct or map (scalars and lists hold no keys to check).
func fieldTypes(t reflect.Type) func(key string) (reflect.Type, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Map:
		return func(string) (reflect.Type, bool) { return t.Elem(), true }
	case reflect.Struct:
		fields := yamlFields(t)
		return func(key string) (reflect.Type, bool) { f, ok := fields[key]; return f, ok }
	default:
		return nil
	}
}

// resolveAlias returns the node an alias points to, or n itself.
func resolveAlias(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return n.Alias
	}
	return n
}

// special reports whether key is no field: a merge key (merged) or an "x-" key at the top
// level.
func special(key, path string) (skip, merged bool) {
	if key == "<<" {
		return true, true
	}
	return path == "" && strings.HasPrefix(key, "x-"), false
}

// mergedKeys checks the mappings merged into a mapping of type t: one, or a list of them.
func mergedKeys(n *yaml.Node, t reflect.Type, path string) []error {
	n = resolveAlias(n)
	if n.Kind != yaml.SequenceNode {
		return unknownKeys(n, t, path)
	}
	var errs []error
	for _, c := range n.Content {
		errs = append(errs, unknownKeys(c, t, path)...)
	}
	return errs
}

// yamlFields maps the YAML keys of struct t (including inlined structs) to their types.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for f := range t.Fields() {
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if opts == "inline" {
			for k, v := range yamlFields(f.Type) {
				fields[k] = v
			}
			continue
		}
		fields[name] = f.Type
	}
	return fields
}

// unknownProblem describes an unknown key, naming its replacement when it is a key of
// format version 1.
func unknownProblem(key *yaml.Node) string {
	replaced := map[string]string{
		"projects":         "renamed to jobs",
		"mode":             "replaced by concurrency (1 = sequential, 0 = all at once)",
		"environments":     "removed: put the variables into repository.env_file",
		"env_file":         "moved to repository.env_file",
		"global":           "replaced by repository and defaults",
		"service_manager":  "renamed to type",
		"systemd_units":    "renamed to units",
		"systemd_scope":    "renamed to scope",
		"stop_services":    "renamed to stop",
		"retention_policy": "replaced by keep",
		"backup_options":   "replaced by exclude, one_file_system, tags, … (see README)",
		"forget_options":   "replaced by keep",
		"prune_options":    "replaced by repository.backend_options, limit_upload, … (see README)",
		"pre_backup_cmd":   "renamed to pre",
		"post_backup_cmd":  "renamed to post",
	}
	problem := fmt.Sprintf("unknown key (line %d)", key.Line)
	if hint, ok := replaced[key.Value]; ok {
		problem += ": " + hint + " in configuration version 2"
	}
	return problem
}

// joinPath returns the dotted path of key below path; either may be empty.
func joinPath(path, key string) string {
	if path == "" || key == "" {
		return path + key
	}
	return path + "." + key
}

// expandNode replaces ${VAR} references in the scalar values below n (not in keys). A
// plain scalar is re-typed after the expansion, so "${LIMIT}" can fill a number.
func expandNode(n *yaml.Node, getenv func(string) string) {
	switch n.Kind {
	case yaml.ScalarNode:
		if v := expand(n.Value, getenv); v != n.Value {
			n.Value = v
			if n.Style == 0 {
				n.Tag = ""
			}
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			expandNode(n.Content[i], getenv)
		}
	default:
		for _, c := range n.Content {
			expandNode(c, getenv)
		}
	}
}

// expand replaces $VAR and ${VAR} with getenv(VAR); ${VAR:-default} uses default when
// VAR is unset or empty, and $$ is a literal $.
func expand(s string, getenv func(string) string) string {
	return os.Expand(s, func(name string) string {
		if name == "$" {
			return "$"
		}
		name, def, hasDefault := strings.Cut(name, ":-")
		if v := getenv(name); v != "" || !hasDefault {
			return v
		}
		return def
	})
}

// homePaths reports paths that start with "~": the shell expands it, YAML does not, so
// it would become a directory named "~" below the config directory.
func (f *File) homePaths() []error {
	paths := map[string][]string{
		"repository.env_file":   {f.Repository.EnvFile},
		"defaults.exclude_file": f.Defaults.ExcludeFile,
	}
	for name, j := range f.Jobs {
		paths["jobs."+name+".compose_file"] = []string{j.ComposeFile}
		paths["jobs."+name+".sources"] = j.Sources
		paths["jobs."+name+".exclude_file"] = j.ExcludeFile
	}
	var errs []error
	for _, field := range sortedKeys(paths) {
		for _, p := range paths[field] {
			if strings.HasPrefix(p, "~") {
				errs = append(errs, &FieldError{Path: field, Problem: fmt.Sprintf(
					"%q: ~ is not expanded, use an absolute path or $HOME", p)})
			}
		}
	}
	return errs
}

// resolvePaths makes the env file, compose files, sources and exclude files absolute,
// relative to the directory of the configuration file at path: under systemd the working
// directory is /.
func (f *File) resolvePaths(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dir := filepath.Dir(abs)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	resolveAll := func(list []string) {
		for i := range list {
			list[i] = resolve(list[i])
		}
	}
	f.Repository.EnvFile = resolve(f.Repository.EnvFile)
	resolveAll(f.Defaults.ExcludeFile)
	for name, j := range f.Jobs {
		j.ComposeFile = resolve(j.ComposeFile)
		resolveAll(j.Sources)
		resolveAll(j.ExcludeFile)
		f.Jobs[name] = j
	}
	return nil
}
