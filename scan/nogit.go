package scan

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"golang.org/x/sync/errgroup"
)

// NoGitScanner is a scanner that absolutely despises git
type NoGitScanner struct {
	BaseScanner
}

// NewNoGitScanner creates and returns a nogit scanner. This is used for scanning files and directories
func NewNoGitScanner(base BaseScanner) *NoGitScanner {
	ngs := &NoGitScanner{
		BaseScanner: base,
	}

	ngs.scannerType = typeNoGitScanner

	// no-git scans should ignore .git folders by default
	// issue: https://github.com/zricethezav/gitleaks/issues/474
	// ngs.cfg.Allowlist
	err := ngs.cfg.Allowlist.IgnoreDotGit()
	if err != nil {
		log.Error(err)
		return nil
	}

	return ngs
}

// Scan kicks off a NoGitScanner Scan
func (ngs *NoGitScanner) Scan() (Report, error) {
	var scannerReport Report

	g, _ := errgroup.WithContext(context.Background())
	paths := make(chan string, 100)

	g.Go(func() error {
		defer close(paths)
		return filepath.Walk(ngs.opts.Path,
			func(path string, fInfo os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if fInfo.Mode().IsRegular() {
					paths <- path
				}
				return nil
			})
	})

	leaks := make(chan Leak, 100)

	for path := range paths {
		p := path
		g.Go(func() error {
			if ngs.cfg.Allowlist.FileAllowed(filepath.Base(p)) ||
				ngs.cfg.Allowlist.PathAllowed(p) {
				return nil
			}

			for _, rule := range ngs.cfg.Rules {
				if rule.HasFileOrPathLeakOnly(p) {
					leak := NewLeak("", "Filename or path offender: "+p, defaultLineNumber)
					leak.File = p
					leak.Rule = rule.Description
					leak.Tags = strings.Join(rule.Tags, ", ")

					if ngs.opts.Verbose {
						leak.Log(ngs.opts.Redact)
					}
					leaks <- leak
				}
			}

			f, err := os.Open(p)
			if err != nil {
				return err
			}
			scanner := bufio.NewScanner(f)
			lineNumber := 0
			for scanner.Scan() {
				lineNumber++
				for _, rule := range ngs.cfg.Rules {
					line := scanner.Text()
					offender := rule.Inspect(line)
					if offender == "" {
						continue
					}
					if ngs.cfg.Allowlist.RegexAllowed(line) ||
						rule.AllowList.FileAllowed(filepath.Base(p)) ||
						rule.AllowList.PathAllowed(p) {
						continue
					}

					if rule.File.String() != "" && !rule.HasFileLeak(filepath.Base(p)) {
						continue
					}
					if rule.Path.String() != "" && !rule.HasFilePathLeak(p) {
						continue
					}

					leak := NewLeak(line, offender, defaultLineNumber)
					leak.File = p
					leak.LineNumber = lineNumber
					leak.Rule = rule.Description
					leak.Tags = strings.Join(rule.Tags, ", ")
					if ngs.opts.Verbose {
						leak.Log(ngs.opts.Redact)
					}
					leaks <- leak
				}
			}
			return f.Close()
		})
	}

	go func() {
		g.Wait()
		close(leaks)
	}()

	for leak := range leaks {
		scannerReport.Leaks = append(scannerReport.Leaks, leak)
	}

	return scannerReport, g.Wait()
}
