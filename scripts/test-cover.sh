#!/usr/bin/env bash
# Self-test for scripts/cover.sh with a stub `go`: pins that build tags
# reach `go list` (tag-only packages must not silently drop out of the
# unit half), that the root package sits in the -coverpkg half and not in
# the unit half, that only test/ packages with BOTH non-test sources and
# tests of their own join -coverpkg (a source-only helper package must
# not), and that the two profiles merge under one mode header.
set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
python3 - "$script_dir/cover.sh" <<'PY'
import json, os, subprocess, sys, tempfile, textwrap
from pathlib import Path

cover = Path(sys.argv[1])
ROOT = 'github.com/example/mod'

with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    bin_dir = root / 'bin'
    bin_dir.mkdir()
    go = bin_dir / 'go'
    # The stub answers `go list -m`, `go list [-tags T] ./...` (a tag-only
    # package appears only when the tag is present) and `go test ...`
    # (writes a tiny profile to the -coverprofile= path), and records
    # every argv.
    go.write_text('#!' + sys.executable + '\n' + textwrap.dedent('''\
        import json, os, re, sys
        from pathlib import Path
        args = sys.argv[1:]
        log = Path(os.environ['GO_CALLS'])
        calls = json.loads(log.read_text()) if log.exists() else []
        calls.append(args)
        log.write_text(json.dumps(calls))
        ROOT = 'github.com/example/mod'
        if args[:2] == ['list', '-m']:
            print(ROOT)
        elif args[0] == 'list':
            tags = args[args.index('-tags') + 1] if '-tags' in args else ''
            if '-f' in args:
                # Evaluate the {{if <cond>}}{{.ImportPath}}{{end}} template
                # over four virtual test/ packages instead of pattern-matching
                # its text, so the assertions below hold for the RULE, not for
                # a substring: x = sources + internal tests, y = pure external
                # test package (no sources), z = sources only (a helper
                # package), w = sources + external tests only.
                tmpl = args[args.index('-f') + 1]
                assert args[-1] == './test/...', args
                m = re.fullmatch(r'\\{\\{if (.*)\\}\\}\\{\\{\\.ImportPath\\}\\}\\{\\{end\\}\\}', tmpl)
                assert m, tmpl
                pkgs = {'x': dict(GoFiles=True, TestGoFiles=True, XTestGoFiles=False),
                        'y': dict(GoFiles=False, TestGoFiles=False, XTestGoFiles=True),
                        'z': dict(GoFiles=True, TestGoFiles=False, XTestGoFiles=False),
                        'w': dict(GoFiles=True, TestGoFiles=False, XTestGoFiles=True)}
                def ev(tokens, fields):
                    t = tokens.pop(0)
                    if t == '(':
                        v = ev(tokens, fields); assert tokens.pop(0) == ')'; return v
                    if t in ('and', 'or'):
                        vals = []
                        while tokens and tokens[0] != ')':
                            vals.append(ev(tokens, fields))
                        return all(vals) if t == 'and' else any(vals)
                    assert t.startswith('.') and t[1:] in fields, t
                    return fields[t[1:]]
                for name, fields in pkgs.items():
                    toks = re.findall(r'\\(|\\)|[^\\s()]+', m.group(1))
                    if ev(toks, fields):
                        assert not toks, toks
                        print(ROOT + '/test/' + name)
                sys.exit(0)
            pkgs = [ROOT, ROOT + '/internal/a', ROOT + '/test/x', ROOT + '/test/y', ROOT + '/test/z']
            if 'special' in tags.split():
                pkgs.append(ROOT + '/internal/tagonly')
            print('\\n'.join(pkgs))
        elif args[0] == 'test':
            prof = [a for a in args if a.startswith('-coverprofile=')][0].split('=', 1)[1]
            name = 'behav' if any(a.startswith('-coverpkg=') for a in args) else 'unit'
            Path(prof).write_text('mode: atomic\\n' + ROOT + '/' + name + '.go:1.1,2.2 1 1\\n')
        else:
            raise AssertionError(args)
        '''))
    go.chmod(0o755)

    def run(name, *flags):
        calls_path = root / (name + '.json')
        out = root / (name + '.out')
        env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'], GO_CALLS=str(calls_path))
        r = subprocess.run(['bash', str(cover), str(out), *flags], env=env, capture_output=True, text=True)
        assert r.returncode == 0, (name, r.stdout, r.stderr)
        calls = json.loads(calls_path.read_text())
        lists = [c for c in calls if c[0] == 'list' and c[1:2] != ['-m'] and '-f' not in c]
        gofile_lists = [c for c in calls if c[0] == 'list' and '-f' in c]
        tests = [c for c in calls if c[0] == 'test']
        assert len(lists) == 1 and len(gofile_lists) == 1 and len(tests) == 2, (name, calls)
        unit, behav = tests
        assert not any(a.startswith('-coverpkg=') for a in unit), (name, unit)
        # root plus exactly the test/ packages with their own sources AND
        # tests (internal or external): x and w. The source-only helper z
        # and the sourceless external test package y must not be there.
        assert '-coverpkg=' + ROOT + ',' + ROOT + '/test/x,' + ROOT + '/test/w' in behav, (name, behav)
        assert ROOT not in unit and not any(a.startswith(ROOT + '/test/') for a in unit), (name, unit)
        assert '.' in behav and './test/...' in behav, (name, behav)
        for f in flags:
            assert f in unit and f in behav, (name, f, tests)
        # both go list calls must see the same tags as go test
        for l in (lists[0], gofile_lists[0]):
            if '-tags' in lists[0]:
                assert l[l.index('-tags') + 1] == lists[0][lists[0].index('-tags') + 1], (name, l)
            else:
                assert '-tags' not in l, (name, l)
        merged = out.read_text().splitlines()
        assert merged[0] == 'mode: atomic' and merged.count('mode: atomic') == 1, merged
        assert any('/unit.go' in l for l in merged) and any('/behav.go' in l for l in merged), merged
        return lists[0], unit

    lst, unit = run('untagged', '-race')
    assert '-tags' not in lst, lst
    assert ROOT + '/internal/tagonly' not in unit, unit
    print('CASE untagged: OK')

    for name, flags in [('tags-separate', ['-race', '-tags', 'special other']),
                        ('tags-equals', ['-tags=special other', '-count=1']),
                        ('dashdash-tags-separate', ['--tags', 'special other']),
                        ('dashdash-tags-equals', ['-race', '--tags=special other'])]:
        lst, unit = run(name, *flags)
        assert lst[lst.index('-tags') + 1] == 'special other', (name, lst)
        assert ROOT + '/internal/tagonly' in unit, (name, unit)
        print(f'CASE {name}: OK')
    print('All cover.sh cases passed')
PY
