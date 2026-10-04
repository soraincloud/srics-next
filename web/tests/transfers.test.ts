import test from 'node:test';
import assert from 'node:assert/strict';
import {chunkSize,prepareTransfer,sendTransfer,transferID} from '../src/transfers.ts';
import type {api} from '../src/api.ts';
test('ordinary empty files finish through the authenticated public namespace without vault or chunks', async()=>{
 const calls:string[]=[];
 const request=(async(path:string,options:RequestInit)=>{
  calls.push(path);
  if(path==='/api/transfers') {
   const task=JSON.parse(options.body as string);
   assert.equal(task.module,'attachments'); assert.equal(task.size,0); assert.deepEqual(task.hashes,[]);
   return {...task,done:[],state:'pending'};
  }
  return {ok:true};
 }) as typeof api;
 await sendTransfer(new File([],'empty.txt'),{id:'ordinary',module:'attachments',parent:'parent',index:0},request,new AbortController().signal,()=>{});
 assert.deepEqual(calls,['/api/transfers','/api/transfers/ordinary/finish']);
});
test('resume hashes the source and sends only unacknowledged chunks',async()=>{
 const file=new File([new Uint8Array(chunkSize),new Uint8Array([9,8,7])],'resume.bin');
 const calls:{path:string;options:RequestInit}[]=[];
 const request=(async(path:string,options:RequestInit)=>{calls.push({path,options});if(path==='/api/vault/transfers'){const body=JSON.parse(options.body as string);assert.equal(body.hashes.length,2);assert.equal(body.size,file.size);return {...body,done:[0],state:'pending'}};return {ok:true}}) as typeof api;
 await sendTransfer(file,{id:'id',module:'files',parent:'',index:0},request,new AbortController().signal,()=>{});
 assert.deepEqual(calls.map(c=>c.path),['/api/vault/transfers','/api/vault/transfers/id/chunks/1','/api/vault/transfers/id/finish']);
 assert.deepEqual([...new Uint8Array(await (calls[1]!.options.body as Blob).arrayBuffer())],[9,8,7]);
});
test('queue registration can complete before any bodies, abort never finishes',async()=>{
 const abort=new AbortController();let bodies=0;
 const request=(async(path:string,options:RequestInit)=>{if(options.method==='POST'&&!path.endsWith('/finish'))return {...JSON.parse(options.body as string),done:[],state:'pending'};bodies++;abort.abort();return {}}) as typeof api;
 const file=new File(['hello'],'file.txt');const task={id:'id',module:'files',parent:'',index:0};
 const prepared=await prepareTransfer(file,task,request,abort.signal,()=>{});assert.equal(bodies,0);
 await assert.rejects(sendTransfer(file,task,request,abort.signal,()=>{},prepared),{name:'AbortError'});
 assert.equal(bodies,1);
 assert.equal(await transferID('a',0),await transferID('a',0));assert.notEqual(await transferID('a',0),await transferID('a',1));
});
