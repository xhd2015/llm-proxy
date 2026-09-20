import {test} from 'node:test';
import assert from 'node:assert/strict';
import {modelGroups, filterModelGroups} from '../config_web/tree.mjs';

test('groups by provider name in config order without mutating model order', () => {
  const doc = {providers:[{name:'secondary',kind:'codex'},{name:'primary',kind:'codex'},{name:'empty'}],models:[
    {provider:'primary',providerModelName:'first'},
    {provider:'secondary',providerModelName:'second',variants:[{clientModelName:'mobile'}]},
    {provider:'primary',providerModelName:'third'},
  ]};
  const before = JSON.stringify(doc);
  const groups = modelGroups(doc);
  assert.deepEqual(groups.map(g=>[g.name,g.models.map(m=>m.index)]),[['secondary',[1]],['primary',[0,2]],['empty',[]]]);
  assert.equal(JSON.stringify(doc),before);
  const found = filterModelGroups(groups,'mobile');
  assert.equal(found.length,1); assert.equal(found[0].name,'secondary');
  assert.equal(found[0].models[0].index,1); assert.equal(found[0].models[0].variants[0].index,0);
  assert.equal(filterModelGroups(groups,'primary')[0].models.length,2);
  assert.equal(filterModelGroups(groups,'second')[0].models[0].variants.length,1);
  assert.deepEqual(filterModelGroups(groups,'not found'),[]);
});

test('unknown and missing providers retain invalid models for repair', () => {
  const groups=modelGroups({providers:[{name:'known'},{name:'known'},null],models:[{provider:'absent'},null,{}, {provider:'known'}]});
  assert.deepEqual(groups.flatMap(g=>g.models.map(m=>m.index)).sort(),[0,1,2,3]);
  assert.equal(groups.find(g=>g.name==='absent').label,'Unknown provider: absent');
  assert.equal(groups.find(g=>g.label==='Missing provider').models.length,2);
  assert.equal(groups[0].models.length,1);assert.equal(groups[1].models.length,0);
  assert.deepEqual(modelGroups(null),[]);
});
