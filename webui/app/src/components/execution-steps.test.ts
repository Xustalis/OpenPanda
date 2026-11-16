// SPDX-License-Identifier: AGPL-3.0-or-later
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { executionAttribution, operationSteps, taskSteps, upsertActivity } from './execution-steps.ts'
import type { Task } from '../api/client.ts'
import { expansion, rememberExpansion } from './execution-expansion.ts'
import { mergeLive } from './task-live.ts'
const event = (id: number, data: object, type = 'agent_event') => ({ id, ts: id, type, data: JSON.stringify(data) })
test('tools pair within attempt executor and nested parent without losing long output', () => {
 const rows = [
  event(1, {ev:'tool_use', id:'same', name:'Bash', attempt_id:'a', executor_id:'mac', input:{command:'test'}}),
  event(2, {ev:'tool_use', id:'same', name:'Read', attempt_id:'a', executor_id:'pi', parent:'child'}),
  event(3, {ev:'tool_result', tool_use_id:'same', attempt_id:'a', executor_id:'pi', parent:'child', content:'pi failed', is_error:true}),
  event(4, {ev:'tool_result', tool_use_id:'same', attempt_id:'a', executor_id:'mac', content:'long'.repeat(4000)}),
  event(5, {ev:'tool_use', id:'same', name:'Retry', attempt_id:'b', executor_id:'mac'}),
 ]
 const steps=taskSteps(rows)
 assert.equal(steps.length,3)
 assert.equal(steps[0]!.output?.length,16000)
 assert.equal(steps[0]!.status,'success')
 assert.equal(steps[1]!.output,'pi failed')
 assert.equal(steps[1]!.status,'failed')
 assert.equal(steps[2]!.status,'unknown')
})
test('keeps every historical step and actual explanation',()=>{
 const rows=Array.from({length:20},(_,i)=>event(i+1,{ev:'thinking',thinking:'actual summary '+i}))
 const steps=taskSteps(rows)
 assert.equal(steps.length,20)
 assert.equal(steps[0]!.output,'actual summary 0')
 assert.equal(steps[19]!.thought,true)
})
test('durable logs take precedence over clipped SSE previews',()=>{
 const full=event(1,{ev:'tool_result',content:'full'.repeat(500)})
 const rows=mergeLive([full],[{id:1,ts:1,type:'agent_event',data:{ev:'tool_result',content:'clipped'}}])
 assert.equal(rows[0]!.raw,full.data)
 assert.equal(rows[0]!.data,undefined)
})
test('activity completion updates the same row preserving order',()=>{
 const a={id:'a',sequence:1,round:1,name:'Read',status:'running',started_at:1000}
 const b={...a,id:'b',sequence:2}
 const rows=upsertActivity([a,b],{...a,status:'success',output:'done'})
 assert.deepEqual(rows.map(r=>r.id),['a','b'])
 assert.equal(operationSteps(rows)[0]!.output,'done')
})
test('manual collapse survives completion remount and scope isolation',()=>{
 assert.equal(expansion('test-new').open,true)
 rememberExpansion('test-run',{open:true,rows:{tool:false}})
 assert.equal(expansion('test-run').rows.tool,false)
 assert.equal(expansion('test-other').rows.tool,undefined)
 rememberExpansion('test-run',{open:false,rows:{}})
 assert.equal(expansion('test-run').open,false)
})
test('actual result and current attempt win over legacy and late executor events',()=>{
 const task = {attempt_id:'new',result:JSON.stringify({executor:'Windows'})} as Task
 const events = [event(1,{executor_id:'Mac',attempt_id:'new'}),
  event(2,{executor_id:'stale',attempt_id:'old'}),event(3,{executor_id:'legacy'}),
  event(4,{attempt_id:'new',profile:{coordination:'IMMEDIATE'}},'adapter_execution_profile')]
 assert.deepEqual(executionAttribution(task,events),{executor:'Windows',mode:'IMMEDIATE'})
 assert.equal(executionAttribution({...task,result:''},events).executor,'Mac')
 assert.equal(executionAttribution({...task,result:''},[event(5,{profile:{coordination:'unknown'}},'adapter_execution_profile')]).mode,'')
})
test('legacy null and scalar event payloads do not break archived timelines', () => {
 const events = ['null', '42', '[]', '"legacy note"'].map((data, i) => ({id: i+1, ts: i+1, type:'progress', data}))
 assert.doesNotThrow(() => taskSteps(events))
})
test('only the latest duplicated result body is suppressed in task details', () => {
 const rows = [event(1, {ok:true, stdout:'first round output'}, 'result'),
  event(2, {ev:'thinking', thinking:'Reviewing the first round'}),
  event(3, {ok:true, stdout:'final output'}, 'result'),
  event(4, {note:'session linked'}, 'progress')]
 const full = taskSteps(rows)
 const detail = taskSteps(rows, false, true)
 assert.equal(detail[0]!.output, 'first round output')
 assert.equal(full[2]!.output, 'final output')
 assert.notEqual(detail[2]!.output, 'final output')
 assert.equal(detail[2]!.status, 'success')
 assert.equal(detail.length, full.length)
})
