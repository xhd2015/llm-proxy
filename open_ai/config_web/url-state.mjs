export const editorTabs = ['models', 'providers', 'raw', 'preview'];
export const editorRunners = ['dsh', 'codex', 'grok'];

export function parseEditorURL(search) {
  const query = String(search || '');
  const params = new URLSearchParams(query.startsWith('?') ? query.slice(1) : query);
  const tab = editorTabs.includes(params.get('tab')) ? params.get('tab') : 'models';
  const runner = editorRunners.includes(params.get('runner')) ? params.get('runner') : 'dsh';
  const file = params.get('file') || '';
  const model = params.get('model') || '';
  const edit = params.get('edit') === 'json' ? 'json' : '';
  return {tab, runner, file, model, edit};
}

export function editorSearch({tab = 'models', runner = 'dsh', file = '', model = '', edit = ''} = {}) {
  const params = new URLSearchParams();
  if (tab && tab !== 'models') params.set('tab', tab);
  if (tab === 'models') {
    if (model) params.set('model', model);
    if (edit === 'json') params.set('edit', 'json');
  }
  if (tab === 'preview') {
    if (runner && runner !== 'dsh') params.set('runner', runner);
    if (file) params.set('file', file);
  }
  const encoded = params.toString();
  return encoded ? '?' + encoded : '';
}

export function previewFileIndex(files, name) {
  if (!name || !Array.isArray(files)) return 0;
  const index = files.findIndex(file => file && file.name === name);
  return index >= 0 ? index : 0;
}
