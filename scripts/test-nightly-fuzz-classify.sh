#!/usr/bin/env bash
# Exercise the actual nightly triage run block offline, with gh/date/sleep stubs.
# Python only supplies fixtures and replaces GitHub expressions; Bash executes
# the workflow code, including issue creation and title-based deduplication.
set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
python3 - "$script_dir/../.github/workflows/nightly-diff-fuzz.yml" <<'PY'
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap

workflow = Path(sys.argv[1]).read_text()
step = workflow.split('      - name: triage and file divergence issue\n', 1)[1]
block = step.split('        run: |\n', 1)[1]
# Stop at the next YAML field/step rather than silently executing unrelated code.
lines = []
for line in block.splitlines():
    if line.strip() and not line.startswith('          '):
        break
    lines.append(line[10:] if line.startswith('          ') else '')
block = '\n'.join(lines) + '\n'
assert 'gh issue create' in block and 'gh issue comment' in block

with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    bin_dir = root / 'bin'
    bin_dir.mkdir()
    gh = bin_dir / 'gh'
    gh.write_text('#!' + sys.executable + '\n' + textwrap.dedent('''\
        import json, os, sys
        from pathlib import Path
        args = sys.argv[1:]
        path = Path(os.environ['GH_CALLS'])
        calls = json.loads(path.read_text()) if path.exists() else []
        calls.append(args)
        path.write_text(json.dumps(calls))
        assert args[:1] == ['issue'], args
        if args[1] == 'list':
            assert args[args.index('--state') + 1] == 'open', args
            attempt = sum(c[1] == 'list' for c in calls)
            print('255' if attempt >= int(os.environ['EXISTING_AFTER']) else 'null')
        elif args[1] not in ('create', 'comment'):
            raise AssertionError(args)
        '''))
    gh.chmod(0o755)
    for name, body in [('date', "printf '%s\\n' 2026-09-07"), ('sleep', 'exit 0')]:
        path = bin_dir / name
        path.write_text('#!/usr/bin/env bash\n' + body + '\n')
        path.chmod(0o755)

    go = bin_dir / 'go'
    go.write_text('#!' + sys.executable + '\n' + textwrap.dedent('''\
        import json, os, sys
        from pathlib import Path
        Path(os.environ['GO_ARGS']).write_text(json.dumps(sys.argv[1:]))
        '''))
    go.chmod(0o755)

    count = 0
    def run(name, logs, label, title, variant='p4', outcome='success',
            contains=(), existing_after=99, run_id='34103646451', replay=None):
        global count
        count += 1
        tree = root / str(count)
        tree.mkdir()
        for filename, content in logs.items():
            (tree / filename).write_text(content)
        replacements = {
            'matrix.variant': variant,
            'matrix.tags': '' if variant == 'p1' else f'wangshu_{variant} wangshu_profile',
            'github.server_url': 'https://github.com',
            'github.repository': 'Liam0205/wangshu',
            'github.run_id': run_id,
            'steps.difffuzz.outcome': outcome,
        }
        script = re.sub(r'\$\{\{\s*(.*?)\s*\}\}', lambda m: replacements[m[1]], block)
        env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'],
                   GH_CALLS=str(tree / 'calls.json'), GO_ARGS=str(tree / 'go-args.json'), EXISTING_AFTER=str(existing_after),
                   SEED_BASE='700', ROUNDS='2000000')
        result = subprocess.run(['bash', '--noprofile', '--norc', '-e', '-o', 'pipefail', '-c', script],
                                cwd=tree, env=env, capture_output=True, text=True)
        assert result.returncode == 0, (name, result.stdout, result.stderr)
        calls = json.loads((tree / 'calls.json').read_text())
        lists = [c for c in calls if c[1] == 'list']
        assert len(lists) == min(3, existing_after), (name, calls)
        assert all(c[c.index('--search') + 1] == f'in:title "{title}"' for c in lists), (name, calls)
        writes = [c for c in calls if c[1] != 'list']
        assert len(writes) == 1, (name, calls)
        write = writes[0]
        if existing_after <= 3:
            assert write[1:3] == ['comment', '255'], (name, write)
            assert f'run {run_id}(tier {variant})' in write[write.index('--body') + 1], (name, write)
        else:
            assert write[1] == 'create', (name, write)
            assert write[write.index('--label') + 1] == label, (name, write)
            assert write[write.index('--title') + 1] == title, (name, write)
            body = write[write.index('--body') + 1]
            for expected in contains:
                assert expected in body, (name, expected, body)
            if replay is not None:
                tags, target, seed = replay
                command = body.split('# replay ', 1)[1].split('\n', 1)[1].split('```', 1)[0]
                executed = subprocess.run(['bash', '--noprofile', '--norc', '-e', '-c', command],
                                          cwd=tree, env=env, capture_output=True, text=True)
                assert executed.returncode == 0, (name, executed.stderr)
                actual = json.loads((tree / 'go-args.json').read_text())
                expected = ['test'] + (['-tags', tags] if tags else []) + [
                    '.', f'-run=^{target}/{seed}$', '-count=1', '-timeout', '60s', '-v']
                assert actual == expected, (name, actual, expected)
        print(f'CASE {name}: OK')

    crash_path = 'testdata/fuzz/FuzzOracleDiffTiered/aadaecf9807d9dc9'
    incident = ('--- FAIL: FuzzOracleDiffTiered (1318.95s)\n'
                '    fuzzing process hung or terminated unexpectedly: exit status 2\n'
                f'    Failing input written to {crash_path}\n'
                'panic: deadlocked!\n')
    crash_title = 'go-fuzz crash (p4): aadaecf9807d9dc9 (2026-09-07)'
    run('issue-255-tiered-only', {'tieredfuzz.log': incident}, 'bug', crash_title,
        contains=(crash_path, '-run="^FuzzOracleDiffTiered/aadaecf9807d9dc9$"',
                  'go test -tags \'wangshu_oracle_cgo wangshu_p4 wangshu_profile\' .'), replay=('wangshu_oracle_cgo wangshu_p4 wangshu_profile', 'FuzzOracleDiffTiered', 'aadaecf9807d9dc9'))
    run('tiered-p3-replay', {'tieredfuzz.log': f'Failing input written to {crash_path}'}, 'bug',
        crash_title.replace('(p4)', '(p3)'), variant='p3',
        contains=('wangshu_oracle_cgo wangshu_p3 wangshu_profile',),
        replay=('wangshu_oracle_cgo wangshu_p3 wangshu_profile', 'FuzzOracleDiffTiered', 'aadaecf9807d9dc9'))
    for log, target, variant, tags in [
        ('gofuzz.log', 'FuzzCompileRun', 'p1', ''),
        ('gofuzz.log', 'FuzzAutoPromote', 'p3', 'wangshu_p3 wangshu_profile'),
        ('gofuzz.log', 'FuzzP4ForceAllPromote', 'p4', 'wangshu_p4 wangshu_profile'),
        ('oraclefuzz.log', 'FuzzOracleDiff', 'p1', 'wangshu_oracle_cgo'),
    ]:
        run(target, {log: f'Failing input written to testdata/fuzz/{target}/abc123'}, 'bug',
            f'go-fuzz crash ({variant}): abc123 (2026-09-07)', variant=variant,
            contains=(f'-run="^{target}/abc123$"', f"go test -tags \'{tags}\' ." if tags else 'go test .'))

    for log in ('gofuzz.log', 'oraclefuzz.log', 'tieredfuzz.log'):
        for marker in ('fuzzing process hung or terminated unexpectedly: exit status 2', 'panic: deadlocked!'):
            run(f'{log}-{marker}', {log: marker}, 'bug', 'go-fuzz worker failure (p4): 2026-09-07',
                contains=('属于被测 fuzz 路径的 bug 信号', '不是基础设施失败'))

    for log in ('difffuzz.log', 'gcstress.log', 'autodiff.log'):
        run(f'{log}-priority', {log: 'DIVERGENCE seed=71 kind=bytediff', 'tieredfuzz.log': incident},
            'bug', 'difftest divergence (p4): seed 71 kind bytediff (2026-09-07)',
            contains=('WANGSHU_FUZZ_SEED_BASE=71',))
    run('corpus-before-worker', {'gofuzz.log': 'panic: deadlocked!', 'tieredfuzz.log': incident},
        'bug', crash_title)
    infra_title = 'nightly-fuzz infra failure (2026-09-07)'
    for variant in ('p1', 'p3', 'p4'):
        run(f'install-skipped-{variant}', {}, 'ci', infra_title, variant=variant, outcome='skipped',
            run_id=f'run-{variant}', contains=('本轮未执行任何差分 fuzz', f'首个报告的 tier:{variant}'))
    for marker in ('context deadline exceeded', 'curl: (28) Operation timed out',
                   'No space left on device', 'The operation was canceled.', 'signal: killed'):
        run(f'unmarked-infra-{marker}', {'tieredfuzz.log': marker}, 'ci', infra_title,
            contains=('差分 fuzz 步骤状态:success',))
    for attempt in (1, 2, 3):
        run(f'bug-existing-on-lookup-{attempt}', {'tieredfuzz.log': incident}, 'bug', crash_title,
            existing_after=attempt)
    for variant in ('p1', 'p3', 'p4'):
        run(f'infra-dedupe-{variant}', {}, 'ci', infra_title, variant=variant,
            existing_after=1, run_id=f'another-run-{variant}')
    print(f'All {count} nightly triage cases passed')
PY
