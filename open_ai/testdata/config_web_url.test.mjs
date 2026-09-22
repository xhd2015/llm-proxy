import {test} from 'node:test';
import assert from 'node:assert/strict';
import {parseEditorURL, editorSearch, previewFileIndex} from '../config_web/url-state.mjs';

test('round-trips preview + runner + file', () => {
  const search = editorSearch({tab: 'preview', runner: 'codex', file: 'llm-proxy-codex.json'});
  assert.equal(search, '?tab=preview&runner=codex&file=llm-proxy-codex.json');
  assert.deepEqual(parseEditorURL(search), {tab: 'preview', runner: 'codex', file: 'llm-proxy-codex.json', model: '', edit: ''});
  assert.deepEqual(parseEditorURL(search.slice(1)), {tab: 'preview', runner: 'codex', file: 'llm-proxy-codex.json', model: '', edit: ''});
});

test('invalid tab and runner fall back to defaults', () => {
  assert.deepEqual(parseEditorURL('?tab=nope&runner=claude&file=x'), {tab: 'models', runner: 'dsh', file: 'x', model: '', edit: ''});
  assert.deepEqual(parseEditorURL(''), {tab: 'models', runner: 'dsh', file: '', model: '', edit: ''});
});

test('omits runner and file unless tab is preview', () => {
  assert.equal(editorSearch({tab: 'models', runner: 'codex', file: 'llm-proxy-codex.json'}), '');
  assert.equal(editorSearch({tab: 'raw', runner: 'codex', file: 'x.json'}), '?tab=raw');
  assert.equal(editorSearch({tab: 'preview', runner: 'dsh', file: ''}), '?tab=preview');
  assert.equal(editorSearch({tab: 'preview', runner: 'grok', file: 'llm-proxy-grok.toml'}), '?tab=preview&runner=grok&file=llm-proxy-grok.toml');
});

test('preview file is selected by name, not index', () => {
  const files = [{name: 'llm-proxy-codex.toml'}, {name: 'llm-proxy-codex.json'}];
  assert.equal(previewFileIndex(files, 'llm-proxy-codex.json'), 1);
  assert.equal(previewFileIndex(files, 'missing'), 0);
  assert.equal(previewFileIndex([], 'llm-proxy-codex.json'), 0);
});

test('models tab carries model and edit=json', () => {
  const search = editorSearch({tab: 'models', model: 'grok-4.6', edit: 'json'});
  assert.equal(search, '?model=grok-4.6&edit=json');
  assert.deepEqual(parseEditorURL(search), {tab: 'models', runner: 'dsh', file: '', model: 'grok-4.6', edit: 'json'});
  assert.equal(editorSearch({tab: 'models', model: 'grok-4.6'}), '?model=grok-4.6');
  assert.deepEqual(parseEditorURL('?model=grok-4.6'), {tab: 'models', runner: 'dsh', file: '', model: 'grok-4.6', edit: ''});
});

test('edit only accepts json, other tabs drop model and edit', () => {
  assert.equal(editorSearch({tab: 'models', edit: 'form'}), '');
  assert.equal(editorSearch({tab: 'preview', model: 'grok-4.6', edit: 'json'}), '?tab=preview');
  assert.equal(editorSearch({tab: 'raw', model: 'grok-4.6', edit: 'json'}), '?tab=raw');
  assert.equal(editorSearch({tab: 'providers', model: 'grok-4.6'}), '?tab=providers');
  assert.deepEqual(parseEditorURL('?tab=preview&model=grok-4.6&edit=json&runner=grok'), {tab: 'preview', runner: 'grok', file: '', model: 'grok-4.6', edit: 'json'});
  assert.deepEqual(parseEditorURL('?edit=form'), {tab: 'models', runner: 'dsh', file: '', model: '', edit: ''});
});
