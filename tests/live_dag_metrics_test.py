# SPDX-License-Identifier: AGPL-3.0-or-later
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('metrics', Path(__file__).resolve().parents[1] / 'scripts/live-dag-metrics.py')
metrics = importlib.util.module_from_spec(spec)
spec.loader.exec_module(metrics)

class LiveMetricsTest(unittest.TestCase):
    def fixture(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        with sqlite3.connect(root / 'runtime.db') as db:
            db.executescript('CREATE TABLE tasks(task_id,stage_id,state,needs_json,resource_json,output_artifact); CREATE TABLE task_events(id,task_id,ts,type,data_json); CREATE TABLE task_checks(id,task_id,phase,check_id,status,exit_code,duration_ms);')
            db.executemany('INSERT INTO tasks VALUES(?,?,?,?,?,?)', [('a','a','done','[]','{}','hash-a'),('b','b','done','["a"]','{}','hash-b')])
            db.executemany('INSERT INTO task_events VALUES(?,?,?,?,?)', [(1,'a',10,'exec_agent_start','{}'),(2,'a',20,'result','{"ok":true}'),(3,'b',20,'exec_agent_start','{}'),(4,'b',30,'result','{"ok":true}')])
        (root / 'processes.jsonl').write_text('')
        return root

    def test_process_sampling_streams_and_deduplicates_pids(self):
        root = self.fixture()
        samples = [
            {'at': 12, 'processes': [{'pid': 7, 'comm': '/bin/claude'}, {'pid': 7, 'comm': 'claude'}, {'pid': 8, 'comm': 'python'}]},
            {'at': 11, 'processes': [{'pid': 7, 'comm': 'claude'}, {'pid': 9, 'comm': 'claude'}]},
        ]
        (root / 'processes.jsonl').write_text('\n'.join(json.dumps(x) for x in samples))
        with mock.patch.object(Path, 'read_text', side_effect=AssertionError('must stream samples')):
            evidence = metrics.collect(root)['process_evidence']
        self.assertEqual(evidence['status'], 'available')
        self.assertEqual(evidence['samples'], 2)
        self.assertEqual(evidence['peak_claude_processes'], 2)
        self.assertEqual(evidence['multi_process_samples'], 1)
        self.assertEqual(evidence['peak_sample'], {'at': 11, 'pids': [7, 9]})

    def test_missing_empty_and_incomplete_sampling_are_distinct(self):
        root = self.fixture()
        path = root / 'processes.jsonl'
        self.assertEqual(metrics.collect(root)['process_evidence']['status'], 'empty')
        path.unlink()
        evidence = metrics.collect(root)['process_evidence']
        self.assertEqual(evidence['status'], 'missing')
        self.assertIsNone(evidence['peak_claude_processes'])
        path.write_text(json.dumps({'at': 10, 'processes': []}) + '\n{"PRIVATE":')
        data = metrics.collect(root)
        self.assertEqual(data['process_evidence']['status'], 'partial')
        self.assertEqual(data['process_evidence']['invalid_samples'], 1)
        self.assertEqual(data['process_evidence']['first_invalid_line'], 2)
        self.assertNotIn('PRIVATE', json.dumps(data))

    def test_same_second_requires_producer_event_before_consumer(self):
        root = self.fixture()
        self.assertTrue(metrics.collect(root)['edges'][0]['dependency_respected'])
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('UPDATE task_events SET id=5 WHERE id=2')
        data = metrics.collect(root)
        self.assertFalse(data['edges'][0]['dependency_respected'])
        self.assertIsNone(data['observed_critical_path_lower_bound_s'])

    def test_valid_zero_processes_is_distinct_from_invalid_evidence(self):
        root = self.fixture()
        path = root / 'processes.jsonl'
        path.write_text(json.dumps({'at': 10, 'processes': [{'pid': 1, 'comm': 'python'}]}))
        evidence = metrics.collect(root)['process_evidence']
        self.assertEqual(evidence['status'], 'available')
        self.assertEqual(evidence['peak_claude_processes'], 0)
        for sample in [None, {'at': True, 'processes': []}, {'at': 10, 'processes': {}},
                       {'at': 10, 'processes': [{'pid': True, 'comm': 'claude'}]},
                       {'at': 10, 'processes': [{'pid': 1, 'comm': None}]}]:
            with self.subTest(sample=sample):
                path.write_text(json.dumps(sample))
                evidence = metrics.collect(root)['process_evidence']
                self.assertEqual(evidence['status'], 'partial')
                self.assertEqual(evidence['samples'], 0)
                self.assertIsNone(evidence['peak_claude_processes'])

    def test_duplicate_stage_ids_are_not_silently_merged(self):
        root = self.fixture()
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('INSERT INTO tasks VALUES(?,?,?,?,?,?)', ('duplicate', 'a', 'done', '[]', '{}', 'hash'))
        data = metrics.collect(root)
        self.assertFalse(data['graph_valid'])
        self.assertFalse(data['execution_evidence_complete'])
        self.assertEqual(data['graph_errors'][0]['kind'], 'invalid_stage_id')
        self.assertIsNone(data['observed_critical_path_lower_bound_s'])

    def test_done_without_execution_evidence_is_not_verified(self):
        root = self.fixture()
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('DELETE FROM task_events WHERE id=4')
        data = metrics.collect(root)
        self.assertTrue(data['all_done'])
        self.assertFalse(data['execution_evidence_complete'])
        self.assertIsNone(data['observed_critical_path_lower_bound_s'])

    def test_missing_dependency_and_cycle_are_reported(self):
        root = self.fixture()
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('UPDATE tasks SET needs_json=? WHERE stage_id=?', ('["missing"]', 'b'))
        data = metrics.collect(root)
        self.assertFalse(data['graph_valid'])
        self.assertEqual(data['edges'][0]['reason'], 'missing_dependency')
        self.assertIsNone(data['observed_critical_path_lower_bound_s'])
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('UPDATE tasks SET needs_json=? WHERE stage_id=?', ('["b"]', 'a'))
            db.execute('UPDATE tasks SET needs_json=? WHERE stage_id=?', ('["a"]', 'b'))
        data = metrics.collect(root)
        self.assertFalse(data['graph_valid'])
        self.assertIn('cycle', [x['kind'] for x in data['graph_errors']])

    def test_done_label_without_success_result_has_no_bound(self):
        root = self.fixture()
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('UPDATE task_events SET data_json=? WHERE id=2', ('{"ok":false}',))
        data = metrics.collect(root)
        self.assertFalse(data['edges'][0]['dependency_respected'])
        self.assertIsNone(data['observed_critical_path_lower_bound_s'])

    def test_deep_graph_does_not_require_python_recursion(self):
        root = self.fixture()
        with sqlite3.connect(root / 'runtime.db') as db:
            db.execute('DELETE FROM tasks')
            db.execute('DELETE FROM task_events')
            for i in range(1100):
                sid = str(1100-i)
                db.execute('INSERT INTO tasks VALUES(?,?,?,?,?,?)', (sid, sid, 'done', json.dumps([str(1101-i)] if i else []), '{}', 'hash'))
                db.executemany('INSERT INTO task_events VALUES(?,?,?,?,?)', [(i*2+1, sid, i*2, 'exec_agent_start', '{}'), (i*2+2, sid, i*2+1, 'result', '{"ok":true}')])
        data = metrics.collect(root)
        self.assertTrue(data['graph_valid'])
        self.assertEqual(data['observed_critical_path_lower_bound_s'], 1100)

    def test_dependency_violation_and_process_count_are_not_hidden(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            with sqlite3.connect(root / 'runtime.db') as db:
                db.executescript('CREATE TABLE tasks(task_id,stage_id,state,needs_json,resource_json,output_artifact); CREATE TABLE task_events(id,task_id,ts,type,data_json); CREATE TABLE task_checks(id,task_id,phase,check_id,status,exit_code,duration_ms);')
                db.executemany('INSERT INTO tasks VALUES(?,?,?,?,?,?)', [('a','a','done','[]','{}','hash-a'),('b','b','done','["a"]','{}','hash-b')])
                db.executemany('INSERT INTO task_events VALUES(?,?,?,?,?)',[(1,'a',10,'exec_agent_start','{}'),(2,'a',20,'result','{"ok":true}'),(3,'b',19,'exec_agent_start','{}'),(4,'b',30,'result','{"ok":true}'),(5,'a',11,'agent_event','{"ev":"tool_use","name":"Bash","input":{"command":"PRIVATE"}}')])
                db.executemany('INSERT INTO task_checks VALUES(?,?,?,?,?,?,?)',[(1,'a','final','c','pass',0,1),(2,'b','final','c','fail',1,1)])
            (root/'processes.jsonl').write_text(json.dumps({'at':10,'processes':[{'pid':1,'comm':'claude'},{'pid':2,'comm':'python'},{'pid':3,'comm':'claude'}]})+'\n')
            before=(root/'runtime.db').read_bytes();data=metrics.collect(root)
            self.assertEqual(before,(root/'runtime.db').read_bytes())
            self.assertEqual(data['process_evidence']['peak_claude_processes'],2)
            self.assertFalse(data['edges'][0]['dependency_respected'])
            self.assertFalse(data['all_final_checks_pass'])
            self.assertEqual(data['historical_failed_final_checks'],1)
            self.assertEqual(data['observed_execution_makespan_s'],20)
            self.assertNotIn('PRIVATE',json.dumps(data))
            self.assertIsNone(data['observed_critical_path_lower_bound_s'])
            with sqlite3.connect(root/'runtime.db') as db:
                db.execute('INSERT INTO task_checks VALUES(?,?,?,?,?,?,?)',(3,'b','final','c','pass',0,1))
            recovered=metrics.collect(root)
            self.assertTrue(recovered['all_final_checks_pass'])
            self.assertEqual(recovered['historical_failed_final_checks'],1)
            with sqlite3.connect(root/'runtime.db') as db:
                db.execute('DELETE FROM tasks')
            self.assertFalse(metrics.collect(root)['all_done'])

if __name__=='__main__':unittest.main()
