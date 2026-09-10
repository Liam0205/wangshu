#!/usr/bin/env bash
# Self-test for scripts/cover.sh with a stub `go`: pins that build tags
# reach `go list` (tag-only packages must not silently drop out of the
# unit half), that the root package sits in the -coverpkg half and not in
# the unit half, and that the two profiles merge under one mode header.
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
        import json, os, sys
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
            pkgs = [ROOT, ROOT + '/internal/a', ROOT + '/test/x']
            if 'special' in tags.split():
                pkgs.append(ROOT + '/internal/tagonly')
            print('\\n'.join(pkgs))
        elif args[0] == 'test':
            prof = [a for a in args if a.startswith('-coverprofile=')][0].split('=', 1)[1]
            name = 'unit' if '-coverpkg=' + ROOT not in args else 'behav'
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
        lists = [c for c in calls if c[0] == 'list' and c[1:2] != ['-m']]
        tests = [c for c in calls if c[0] == 'test']
        assert len(lists) == 1 and len(tests) == 2, (name, calls)
        unit, behav = tests
        assert '-coverpkg=' + ROOT not in unit and '-coverpkg=' + ROOT in behav, (name, tests)
        assert ROOT not in unit and ROOT + '/test/x' not in unit, (name, unit)
        assert '.' in behav and './test/...' in behav, (name, behav)
        for f in flags:
            assert f in unit and f in behav, (name, f, tests)
        merged = out.read_text().splitlines()
        assert merged[0] == 'mode: atomic' and merged.count('mode: atomic') == 1, merged
        assert any('/unit.go' in l for l in merged) and any('/behav.go' in l for l in merged), merged
        return lists[0], unit

    lst, unit = run('untagged', '-race')
    assert '-tags' not in lst, lst
    assert ROOT + '/internal/tagonly' not in unit, unit
    print('CASE untagged: OK')

    for name, flags in [('tags-separate', ['-race', '-tags', 'special other']),
                        ('tags-equals', ['-tags=special other', '-count=1'])]:
        lst, unit = run(name, *flags)
        assert lst[lst.index('-tags') + 1] == 'special other', (name, lst)
        assert ROOT + '/internal/tagonly' in unit, (name, unit)
        print(f'CASE {name}: OK')
    print('All cover.sh cases passed')
PY
