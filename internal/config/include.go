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

// include adds the jobs of the files that f.Include names to f.Jobs and returns the files
// it read. path is the configuration file: patterns and the paths in a job file are
// resolved like its own, against the directory of the file they are written in. A
// pattern that matches nothing is fine (an empty jobs.d); a path without a pattern must
// exist. Job files do not include others.
func (f *File) include(path string, getenv func(string) string) ([]string, []error) {
	if len(f.Include) == 0 {
		return nil, nil
	}
	dir, err := fileDir(path)
	if err != nil {
		return nil, []error{err}
	}
	self := filepath.Join(dir, filepath.Base(path))
	selfInfo, err := os.Stat(self)
	if err != nil {
		return nil, []error{fmt.Errorf("config: %w", err)}
	}
	files, errs := includedFiles(dir, f.Include, selfInfo)
	origin := map[string]string{} // lowercase job name → file it is defined in
	for name := range f.Jobs {
		origin[strings.ToLower(name)] = self
	}
	for _, file := range files {
		jobs, fileErrs := loadJobFile(file, getenv)
		errs = append(errs, fileErrs...)
		errs = append(errs, f.addJobs(file, jobs, origin)...)
	}
	return files, errs
}

// includedFiles returns the files that patterns name, below dir when relative, each once
// and in the order of the patterns (the matches of one pattern sorted). The
// configuration file self is left out, also when a pattern spells or links it
// differently, so "*.yaml" next to it works.
func includedFiles(dir string, patterns []string, self os.FileInfo) ([]string, []error) {
	var files []string
	var errs []error
	resolve := resolver(dir)
	for _, pattern := range patterns {
		matches, err := match(resolve(pattern))
		if err != nil {
			errs = append(errs, &FieldError{Path: "include", Problem: err.Error()})
			continue
		}
		for _, m := range matches {
			if !isFile(m, self) && !slices.Contains(files, m) {
				files = append(files, m)
			}
		}
	}
	return files, errs
}

// isFile reports whether path is the file that info describes. A path that cannot be
// read is not: reading it as a job file reports the error.
func isFile(path string, info os.FileInfo) bool {
	other, err := os.Stat(path)
	return err == nil && os.SameFile(other, info)
}

// match returns the files that pattern matches, cleaned; a path without glob characters
// must exist.
func match(pattern string) ([]string, error) {
	if pattern == "" {
		return nil, errors.New("must not contain empty entries")
	}
	pattern = filepath.Clean(pattern)
	if !strings.ContainsAny(pattern, `*?[\`) {
		if _, err := os.Stat(pattern); err != nil {
			return nil, err
		}
		return []string{pattern}, nil
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", pattern, err)
	}
	return matches, nil
}

// loadJobFile reads the jobs of an included file, checked, expanded and with their paths
// resolved like those of the configuration file. Every problem names the file. An empty
// file (every job commented out) has no jobs, like an empty jobs.d.
func loadJobFile(path string, getenv func(string) string) (map[string]JobConfig, []error) {
	root, err := readFile(path)
	if errors.Is(err, ErrEmpty) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	errs := jobFileKeys(root)
	expandNode(root, getenv)
	var inc includeFile
	if err := root.Decode(&inc); err != nil {
		return nil, inFile(path, append(errs, decodeErrors(err, root)...))
	}
	paths := map[string][]string{}
	addJobPaths(paths, inc.Jobs)
	errs = append(errs, tildeErrors(paths)...)
	dir, err := fileDir(path)
	if err != nil {
		return nil, append(inFile(path, errs), err)
	}
	resolveJobs(inc.Jobs, dir)
	return inc.Jobs, inFile(path, errs)
}

// jobFileKeys returns the unknown keys of a job file; a top-level one gets a hint.
func jobFileKeys(root *yaml.Node) []error {
	errs := unknownKeys(root, reflect.TypeFor[includeFile](), "")
	for _, err := range errs {
		var fe *FieldError
		if errors.As(err, &fe) && !strings.Contains(fe.Path, ".") {
			fe.Problem += ": a job file holds only jobs (and x- keys for anchors)"
		}
	}
	return errs
}

// addJobs adds the jobs of file to f.Jobs; a name that another file already defines (in
// any case) is an error. origin maps the lowercase names to their files.
func (f *File) addJobs(file string, jobs map[string]JobConfig, origin map[string]string,
) []error {
	if f.Jobs == nil {
		f.Jobs = map[string]JobConfig{}
	}
	var errs []error
	for _, name := range sortedKeys(jobs) {
		key := strings.ToLower(name)
		if other, dup := origin[key]; dup {
			problem := fmt.Sprintf("defined in %s and in %s", other, file)
			if other == file {
				problem = "duplicate job name in " + file + " (names are case-insensitive)"
			}
			errs = append(errs, &FieldError{Path: "jobs." + key, Problem: problem})
			continue
		}
		origin[key] = file
		f.Jobs[name] = jobs[name]
	}
	return errs
}

// inFile prefixes every error with the file it is about; each stays its own error, so
// that they are reported one per line.
func inFile(path string, errs []error) []error {
	out := make([]error, len(errs))
	for i, err := range errs {
		out[i] = fmt.Errorf("%s: %w", path, err)
	}
	return out
}
