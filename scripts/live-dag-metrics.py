#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
"""Read-only real-DAG evidence export. No prompts, tool arguments or secrets.
Usage: python3 scripts/live-dag-metrics.py RUN_DIR
Process sampling is independent evidence, not inferred from task state labels.
"""
import collections
import datetime
import json
import math
import pathlib
import sqlite3
import sys


def process_evidence(path):
    """Stream samples; absent or corrupt evidence must not imply zero overlap."""
    result = {'status': 'missing', 'peak_claude_processes': None, 'samples': 0,
              'invalid_samples': 0, 'first_invalid_line': None,
              'multi_process_samples': 0, 'three_process_samples': 0,
              'first_overlap_utc': None, 'peak_sample': None}
    try:
        stream = path.open(encoding='utf-8')
    except FileNotFoundError:
        return result
    first_overlap = None
    with stream:
        for line_number, line in enumerate(stream, 1):
            if not line.strip():
                continue
            try:
                sample = json.loads(line)
                at, processes = sample['at'], sample['processes']
                if type(at) not in (int, float) or not math.isfinite(at) or not isinstance(processes, list):
                    raise ValueError('invalid sample')
                datetime.datetime.fromtimestamp(at, datetime.timezone.utc)
                pids = set()
                for process in processes:
                    pid, comm = process['pid'], process['comm']
                    if type(pid) is not int or pid <= 0 or not isinstance(comm, str):
                        raise ValueError('invalid process')
                    # Count native processes, never adapters, MCP workers or state rows.
                    if pathlib.Path(comm).name == 'claude':
                        pids.add(pid)
            except (ValueError, TypeError, KeyError, OverflowError, OSError):
                result['invalid_samples'] += 1
                if result['first_invalid_line'] is None:
                    result['first_invalid_line'] = line_number
                continue
            result['samples'] += 1
            count = len(pids)
            if result['peak_claude_processes'] is None or count > result['peak_claude_processes']:
                result['peak_claude_processes'] = count
                result['peak_sample'] = {'at': at, 'pids': sorted(pids)}
            if count >= 2:
                result['multi_process_samples'] += 1
                first_overlap = at if first_overlap is None else min(first_overlap, at)
            if count >= 3:
                result['three_process_samples'] += 1
    result['status'] = 'partial' if result['invalid_samples'] else ('available' if result['samples'] else 'empty')
    if first_overlap is not None:
        result['first_overlap_utc'] = datetime.datetime.fromtimestamp(first_overlap, datetime.timezone.utc).isoformat()
    return result


def graph_evidence(stages):
    """Validate topology and event order before deriving a critical-path bound."""
    by_id = {stage['stage_id']: stage for stage in stages}
    errors = []
    for sid, count in collections.Counter(stage['stage_id'] for stage in stages).items():
        if not sid or count != 1:
            errors.append({'kind': 'invalid_stage_id', 'stage_id': sid, 'count': count})
    edges, durations = [], {}
    successors = collections.defaultdict(list)
    indegree = {sid: 0 for sid in by_id}
    for stage in stages:
        sid = stage['stage_id']
        if stage['starts'] and stage['ends']:
            start, end = stage['starts'][0], stage['ends'][-1]
            if end['event'] > start['event'] and end['ts'] >= start['ts'] and end['ok'] is True:
                durations[sid] = end['ts'] - start['ts']
        for dep in stage['needs']:
            parent = by_id.get(dep)
            producer = parent['ends'][-1] if parent and parent['ends'] else None
            consumer = stage['starts'][0] if stage['starts'] else None
            reason = None
            if parent is None:
                reason = 'missing_dependency'
                errors.append({'kind': reason, 'from': dep, 'to': sid})
            elif parent['state'] != 'done' or not producer or producer['ok'] is not True:
                reason = 'producer_not_completed'
            elif not consumer:
                reason = 'consumer_not_started'
            elif producer['event'] >= consumer['event'] or producer['ts'] > consumer['ts']:
                reason = 'event_order_violation'
            edges.append({'from': dep, 'to': sid, 'dependency_respected': reason is None,
                          'reason': reason, 'producer_event': producer['event'] if producer else None,
                          'consumer_event': consumer['event'] if consumer else None})
            if parent is not None:
                successors[dep].append(sid)
                indegree[sid] += 1
    # Kahn's traversal avoids recursion limits on large, valid plans.
    ready = collections.deque(sid for sid, degree in indegree.items() if degree == 0)
    path = {sid: durations.get(sid, 0) for sid in by_id}
    visited = 0
    while ready:
        sid = ready.popleft()
        visited += 1
        for child in successors[sid]:
            path[child] = max(path[child], path[sid] + durations.get(child, 0))
            indegree[child] -= 1
            if indegree[child] == 0:
                ready.append(child)
    if visited != len(by_id):
        errors.append({'kind': 'cycle', 'blocked_stages': sorted(sid for sid, degree in indegree.items() if degree)})
    complete = bool(stages) and all(stage['state'] == 'done' for stage in stages)
    verified = complete and not errors and len(durations) == len(stages) and all(edge['dependency_respected'] for edge in edges)
    return {'edges': edges, 'graph_valid': not errors, 'graph_errors': errors, 'all_done': complete,
            'execution_evidence_complete': verified, 'observed_stage_duration_s': durations,
            'observed_work_sum_s': sum(durations.values()),
            'observed_critical_path_lower_bound_s': max(path.values(), default=0) if verified else None}


def collect(root):
    root = pathlib.Path(root).resolve()
    conn = sqlite3.connect((root / 'runtime.db').as_uri() + '?mode=ro', uri=True)
    conn.row_factory = sqlite3.Row
    conn.execute('PRAGMA query_only=ON')
    conn.execute('BEGIN')
    stages = []
    for task in conn.execute('SELECT task_id,stage_id,state,needs_json,resource_json,output_artifact FROM tasks ORDER BY stage_id'):
        events = list(conn.execute('SELECT id,ts,type,data_json FROM task_events WHERE task_id=? ORDER BY id', (task['task_id'],)))
        tools = collections.Counter(); usage = collections.Counter(); loaded = []; models = []; starts = []; ends = []
        for event in events:
            data = json.loads(event['data_json'] or '{}')
            if event['type'] == 'exec_agent_start':
                starts.append({'event': event['id'], 'ts': event['ts'], 'prompt_bytes': data.get('prompt_bytes')})
                loaded.append({'event': event['id'], 'skills': data.get('loaded_skills', [])})
            if event['type'] == 'result':
                ends.append({'event': event['id'], 'ts': event['ts'], 'ok': data.get('ok')})
            if event['type'] == 'agent_event':
                if data.get('ev') == 'tool_use': tools[data.get('name', 'unknown')] += 1
                if data.get('ev') == 'model_observed':
                    try: observed = json.loads(data.get('text', '{}'))
                    except (ValueError, TypeError): observed = {}
                    models.append({'event': event['id'], 'observed': observed})
            if event['type'] == 'agent_usage':
                for key in ('input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_write_tokens'):
                    if isinstance(data.get(key), int): usage[key] += data[key]
        checks = [dict(x) for x in conn.execute('SELECT id,phase,check_id,status,exit_code,duration_ms FROM task_checks WHERE task_id=? ORDER BY id', (task['task_id'],))]
        stages.append({**dict(task), 'needs': json.loads(task['needs_json'] or '[]'),
                       'critical_path_rank': json.loads(task['resource_json'] or '{}').get('critical_path_rank'),
                       'starts': starts, 'ends': ends, 'tools': dict(tools), 'usage_observed': dict(usage),
                       'loaded_skills': loaded, 'native_models': models, 'checks': checks})
        del stages[-1]['needs_json']; del stages[-1]['resource_json']
    conn.close()
    graph = graph_evidence(stages)
    latest_final = []
    for stage in stages:
        latest = {}
        for check in stage['checks']:
            if check['phase'] == 'final': latest[check['check_id']] = check
        latest_final.append(list(latest.values()))
    first_start = min((e['ts'] for x in stages for e in x['starts']), default=None)
    last_end = max((e['ts'] for x in stages for e in x['ends']), default=None)
    result = {'run': str(root), 'stages': stages, **graph,
              'all_final_checks_pass': bool(stages) and all(checks and all(c['status'] == 'pass' for c in checks) for checks in latest_final),
              'historical_failed_checks': sum(c['status']=='fail' for x in stages for c in x['checks']),
              'historical_failed_final_checks': sum(c['phase']=='final' and c['status']=='fail' for x in stages for c in x['checks']),
              'observed_execution_makespan_s': last_end-first_start if first_start is not None and last_end is not None else None,
              'process_evidence': process_evidence(root / 'processes.jsonl'),
              'limitations': ['Second-resolution database timestamps; dependency order also requires increasing database event IDs.', 'Process overlap proves live processes, not simultaneous provider GPU compute; sampling must be scoped to this run.', 'Missing, empty or partial sampling is not complete concurrency evidence.', 'Stage durations span first start to last successful result and include retries and operator pauses; they are not pure service times.', 'Critical-path bound requires complete successful execution evidence and a valid dependency order; it uses observed stage spans, not a theoretical optimum or matched native A/B benchmark.']}
    return result

if __name__ == '__main__':
    print(json.dumps(collect(sys.argv[1]), indent=2, ensure_ascii=False))
