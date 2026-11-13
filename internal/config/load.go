package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// Load reads the file at path and the job files it includes, and returns the validated
// Plan. ${VAR} references in values are expanded with getenv, relative paths are resolved
// against the directory of the file they are in and the repository's env file is read.
// All problems are returned together (errors.Join).
func Load(path string, getenv func(string) string) (*Plan, error) {
	root, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	keyErrs := unknownKeys(root, reflect.TypeFor[File](), "")
	keyErrs = append(keyErrs, expandNode(root, getenv, "")...)
	var f File
	if err := root.Decode(&f); err != nil {
		return nil, fmt.Errorf("config: %w",
			errors.Join(append(keyErrs, decodeErrors(err, root)...)...))
	}
	keyErrs = append(keyErrs, f.homePaths()...)
	if err := f.resolvePaths(path); err != nil {
		return nil, err
	}
	included, includeErrs := f.include(path, getenv)
	plan, errs := f.plan()
	if err := errors.Join(slices.Concat(keyErrs, includeErrs, errs)...); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	plan.Included = included
	return plan, nil
}

// readFile parses the YAML file at path and returns its top-level node; errors name the
// file.
func readFile(path string) (*yaml.Node, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
		return nil, fmt.Errorf("%s: %w", path, ErrFormat)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err // an *fs.PathError: it names the operation and the file
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s: %w", path, ErrEmpty)
	}
	return doc.Content[0], nil
}

// decodeErrors splits a YAML decoding error into one error per problem, each with the
// path of the value below root it is about (the decoder reports only its position).
func decodeErrors(err error, root *yaml.Node) []error {
	var load *yaml.LoadErrors
	if !errors.As(err, &load) {
		return []error{err}
	}
	paths := map[[2]int]string{}
	valuePaths(root, "", paths)
	errs := make([]error, 0, len(load.Errors))
	for _, e := range load.Errors {
		path, ok := paths[[2]int{e.Mark.Line, e.Mark.Column}]
		if !ok {
			path = fmt.Sprintf("line %d", e.Mark.Line)
		}
		errs = append(errs, &FieldError{
			Path: path, Problem: fmt.Sprintf("%s (line %d)", e.Message, e.Mark.Line),
		})
	}
	return errs
}

// valuePaths records the path of every value below n by its line and column. List
// entries get the path of their list.
func valuePaths(n *yaml.Node, path string, paths map[[2]int]string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			child, value := keyPath(path, n.Content[i].Value), n.Content[i+1]
			paths[[2]int{value.Line, value.Column}] = child
			valuePaths(value, child, paths)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			paths[[2]int{c.Line, c.Column}] = path
			valuePaths(c, path, paths)
		}
	}
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
		key, child := n.Content[i], keyPath(path, n.Content[i].Value)
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

// keyPath returns the path of key below path. Job names are lowercase, as in every
// message after lowerJobNames.
func keyPath(path, key string) string {
	if path == "jobs" {
		key = strings.ToLower(key)
	}
	return joinPath(path, key)
}

// expandNode replaces ${VAR} references in the scalar values below n (not in keys) and
// returns an error, with the field path below path, for every variable that is unset or
// empty and has no default: a path such as "${DATA}/db" would otherwise silently become
// "/db". A plain scalar is re-typed after the expansion, so "${LIMIT}" can fill a number.
func expandNode(n *yaml.Node, getenv func(string) string, path string) []error {
	var errs []error
	switch n.Kind {
	case yaml.ScalarNode:
		errs = expandScalar(n, getenv, path)
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			errs = append(errs, expandNode(n.Content[i+1], getenv,
				keyPath(path, n.Content[i].Value))...)
		}
	default:
		for _, c := range n.Content {
			errs = append(errs, expandNode(c, getenv, path)...)
		}
	}
	return errs
}

// expandScalar expands the value of the scalar node n (see expandNode).
func expandScalar(n *yaml.Node, getenv func(string) string, path string) []error {
	v, missing := expand(n.Value, getenv)
	errs := make([]error, 0, len(missing))
	for _, name := range missing {
		errs = append(errs, &FieldError{Path: path, Problem: fmt.Sprintf(
			"$%s is not set (line %d); write ${%s:-} for an optional value, or $$ for "+
				"a literal $", name, n.Line, name)})
	}
	if v != n.Value {
		n.Value = v
		if n.Style == 0 {
			n.Tag = ""
		}
	}
	return errs
}

// expand replaces $VAR and ${VAR} with getenv(VAR); ${VAR:-default} uses default when
// VAR is unset or empty, and $$ is a literal $. It also returns the variables without a
// default that are unset or empty (getenv cannot tell them apart).
func expand(s string, getenv func(string) string) (string, []string) {
	var missing []string
	out := os.Expand(s, func(name string) string {
		if name == "$" {
			return "$"
		}
		name, def, hasDefault := strings.Cut(name, ":-")
		v := getenv(name)
		switch {
		case v != "":
			return v
		case hasDefault:
			return def
		}
		missing = append(missing, name)
		return ""
	})
	return out, missing
}

// homePaths reports paths that start with "~": the shell expands it, YAML does not, so
// it would become a directory named "~" below the config directory.
func (f *File) homePaths() []error {
	paths := map[string][]string{
		"repository.env_file":   {f.Repository.EnvFile},
		"defaults.exclude_file": f.Defaults.ExcludeFile,
		"include":               f.Include,
	}
	addJobPaths(paths, f.Jobs)
	return tildeErrors(paths)
}

// addJobPaths adds the paths of jobs to paths, by field.
func addJobPaths(paths map[string][]string, jobs map[string]JobConfig) {
	for name, j := range jobs {
		job := keyPath("jobs", name)
		paths[job+".compose_file"] = []string{j.ComposeFile}
		paths[job+".sources"] = j.Sources
		paths[job+".exclude_file"] = j.ExcludeFile
	}
}

// tildeErrors reports the paths, by field, that start with "~".
func tildeErrors(paths map[string][]string) []error {
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
	dir, err := fileDir(path)
	if err != nil {
		return err
	}
	resolve := resolver(dir)
	f.Repository.EnvFile = resolve(f.Repository.EnvFile)
	resolveEach(resolve, f.Defaults.ExcludeFile)
	resolveJobs(f.Jobs, dir)
	return nil
}

// fileDir returns the absolute directory of the file at path.
func fileDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return filepath.Dir(abs), nil
}

// resolveJobs makes the compose file, sources and exclude files of jobs absolute,
// relative to dir.
func resolveJobs(jobs map[string]JobConfig, dir string) {
	resolve := resolver(dir)
	for name, j := range jobs {
		j.ComposeFile = resolve(j.ComposeFile)
		resolveEach(resolve, j.Sources)
		resolveEach(resolve, j.ExcludeFile)
		jobs[name] = j
	}
}

// resolver returns a function that makes a relative path absolute below dir; "" stays.
func resolver(dir string) func(string) string {
	return func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
}

func resolveEach(resolve func(string) string, list []string) {
	for i := range list {
		list[i] = resolve(list[i])
	}
}
