import {modelGroups, filterModelGroups, modelTitle} from './tree.mjs';
import {parseEditorURL, editorSearch, previewFileIndex} from './url-state.mjs';

const $ = (id) => document.getElementById(id);
const initialUI = parseEditorURL(location.search);
let text = '', saved = '', revision = '', tab = initialUI.tab, selected = 0, report = null;
let previewRunner = initialUI.runner, previewFile = 0, previewFileName = initialUI.file;
let editJSON = initialUI.edit === 'json';
const mergeNativeStorageKey = 'llm-proxy.merge-native';
function mergeNativeEnabled() {
  try { return localStorage.getItem(mergeNativeStorageKey) !== '0'; } catch { return true; }
}
let selectedVariant = -1, selectedProvider = -1;
const collapsedProviders = new Set(), expandedModels = new Set();
let generation = 0, timer, busy = false;
const fieldErrors = new Map();
const object = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const node = (tag, content, className) => {
  const el = document.createElement(tag);
  if (content !== undefined) el.textContent = content;
  if (className) el.className = className;
  return el;
};
const button = (label, action, className) => {
  const el = node('button', label, className);
  el.onclick = action;
  return el;
};
function message(value) { $('message').textContent = value; }
function state() {
  $('status').textContent = busy ? 'Saving…' : text === saved ? 'Saved' : 'Unsaved changes';
  $('save').disabled = busy || text === saved || !report?.valid || fieldErrors.size > 0;
  $('copy').disabled = $('download').disabled = !report?.previews?.[previewRunner]?.content;
  renderPreview();
  $('reload').disabled = $('validate').disabled = busy;
  document.querySelector('main').inert = busy;
  document.querySelector('nav').inert = busy;
  syncURL();
}
function syncURL() {
  const file = tab === 'preview' ? (selectedPreviewFile()?.name || previewFileName) : '';
  const open = tab === 'models' ? openModelRoute() : null;
  const search = editorSearch({tab, runner: previewRunner, file, model: open?.name || '', edit: editJSON && tab === 'models' ? 'json' : ''});
  const next = location.pathname + search + location.hash;
  if (next !== location.pathname + location.search + location.hash) history.replaceState(null, '', next);
}
async function api(path, body) {
  const response = await fetch(`/api/${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: body === undefined ? {} : {'Content-Type': 'application/json'},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    if (response.status === 422) { const value = await response.json(); showReport(value); throw new Error('Fix validation errors before saving.'); }
    throw new Error(await response.text());
  }
  return response.json();
}
function previewFiles() {
  const preview = report?.previews?.[previewRunner];
  if (Array.isArray(preview?.files) && preview.files.length) return preview.files;
  return preview?.content ? [{name: `llm-proxy-${previewRunner}.${preview.format}`, format: preview.format, content: preview.content}] : [];
}
function selectedPreviewFile() {
  const files = previewFiles();
  previewFile = previewFileIndex(files, previewFileName);
  return files[previewFile];
}
function previewFileLabel(file) {
  if (file.targetState === 'missing') return file.name + ' · new';
  if (file.targetState === 'differ') return file.name + ' · changed';
  return file.name;
}
function showCatalogDiff(preview, file) {
  if (preview?.error || previewRunner !== 'codex' || file?.name !== 'llm-proxy-codex.json') return false;
  if (file.targetState === 'missing') return true;
  return file.targetState === 'differ' && typeof file.targetContent === 'string';
}
function catalogStatus(file) {
  if (previewRunner !== 'codex' || file?.name !== 'llm-proxy-codex.json') return '';
  if (file.targetState === 'missing') return `Not installed. Create writes ${file.targetPath}.`;
  if (file.targetState === 'same') return 'Installed file matches this preview.';
  if (file.targetState === 'differ' && typeof file.targetContent === 'string') return `Update will replace ${file.targetPath}.`;
  if (file.targetState === 'differ' && file.note) return file.note;
  return '';
}
const previewSideBySide = window.matchMedia('(min-width: 761px)');
let diffEditor, diffOriginal, diffModified, diffToken = 0, monacoLoad;
previewSideBySide.addEventListener('change', () => {
  diffEditor?.updateOptions({ renderSideBySide: previewSideBySide.matches });
});
function loadMonaco() {
  if (!monacoLoad) {
    window.MonacoEnvironment = {
      getWorker(_id, label) {
        const url = label === 'json' ? '/vs/language/json/json.worker.js' : '/vs/editor/editor.worker.js';
        return new Worker(url, { type: 'module', name: label });
      },
    };
    if (!document.querySelector('link[href="/vs/editor/editor.main.css"]')) {
      const link = document.createElement('link');
      link.rel = 'stylesheet';
      link.href = '/vs/editor/editor.main.css';
      document.head.append(link);
    }
    monacoLoad = import('/vs/editor/editor.main.js').then(mod => mod.default);
  }
  return monacoLoad;
}
function ensureCatalogDiff(monaco, file) {
  const originalText = file.targetState === 'missing' ? '' : file.targetContent;
  const modifiedText = file.content || '';
  if (!diffEditor) {
    diffEditor = monaco.editor.createDiffEditor($('preview-diff'), {
      readOnly: true,
      originalEditable: false,
      renderSideBySide: previewSideBySide.matches,
      automaticLayout: true,
      scrollBeyondLastLine: false,
      minimap: { enabled: false },
      wordWrap: 'on',
    });
    diffOriginal = monaco.editor.createModel(originalText, 'json');
    diffModified = monaco.editor.createModel(modifiedText, 'json');
    diffEditor.setModel({ original: diffOriginal, modified: diffModified });
  } else {
    if (diffOriginal.getValue() !== originalText) diffOriginal.setValue(originalText);
    if (diffModified.getValue() !== modifiedText) diffModified.setValue(modifiedText);
    diffEditor.updateOptions({ renderSideBySide: previewSideBySide.matches });
  }
  diffEditor.layout();
}

// The model JSON editor edits exactly one model or variant object. Its Monaco
// models use dedicated inmemory URIs so the schema registration below never
// applies to the preview diff models on the same page.
const modelEditURIPrefix = 'inmemory://llm-proxy/model-edit/';
let jsonEditor, jsonModel, jsonModelKey = '', jsonInvalid = false, jsonApplying = false, schemaLoad, jsonApplyTimer, jsonPoll, jsonAppliedText = '';
const canonicalJSON = (text) => JSON.stringify(sortJSON(JSON.parse(text)));
function sortJSON(value) {
  if (Array.isArray(value)) return value.map(sortJSON);
  if (object(value)) {
    const out = {};
    for (const key of Object.keys(value).sort()) out[key] = sortJSON(value[key]);
    return out;
  }
  return value;
}
function loadModelSchema(monaco) {
  if (!schemaLoad) {
    schemaLoad = fetch('/model.schema.json').then(response => {
      if (!response.ok) throw new Error('model schema HTTP ' + response.status);
      return response.json();
    }).then(schemaDoc => {
      // The vendored bundle exports the JSON feature's jsonDefaults singleton
      // directly (Monaco 0.56 has no languages.json namespace). The served
      // document keeps model/variant under definitions (mirroring the Go
      // structs); each editor kind gets a plain root schema built from its
      // definition so the worker needs no top-level $ref resolution. Nested
      // $refs (inputs, reasoning, variants) resolve inside the same document.
      const root = def => ({...def, definitions: schemaDoc.definitions, $schema: schemaDoc.$schema});
      monaco.jsonDefaults?.setDiagnosticsOptions({
        validate: true,
        allowComments: false,
        enableSchemaRequest: false,
        schemaValidation: 'error',
        schemas: [
          {uri: schemaFileURI('#model'), fileMatch: ['model-edit/model'], schema: root(schemaDoc.definitions.model)},
          {uri: schemaFileURI('#variant'), fileMatch: ['model-edit/variant'], schema: root(schemaDoc.definitions.variant)},
        ],
      });
    });
  }
  return schemaLoad;
}
function schemaFileURI(fragment) {
  return 'inmemory://llm-proxy/model.schema.json' + fragment;
}
function currentModelSlot(doc) {
  if (tab !== 'models' || !doc || selectedProvider >= 0) return null;
  const base = doc.models?.[selected];
  if (!object(base)) return null;
  if (selectedVariant >= 0) {
    return Array.isArray(base.variants) && object(base.variants[selectedVariant]) ? {list: base.variants, index: selectedVariant, kind: 'variant'} : null;
  }
  return {list: doc.models, index: selected, kind: 'model'};
}
function routeNameOf(item, kind) {
  if (!object(item)) return '';
  return kind === 'variant' ? String(item.clientModelName || '') : String(item.clientModelName || item.providerModelName || '');
}
function openModelRoute() {
  const slot = currentModelSlot(parsed());
  return slot ? {name: routeNameOf(slot.list[slot.index], slot.kind), kind: slot.kind} : null;
}
function findRoute(doc, name) {
  if (!name || !Array.isArray(doc?.models)) return null;
  for (const [index, model] of doc.models.entries()) {
    if (!object(model)) continue;
    if (routeNameOf(model, 'model') === name) return {index, variant: -1};
    if (Array.isArray(model.variants)) {
      const variant = model.variants.findIndex(v => object(v) && routeNameOf(v, 'variant') === name);
      if (variant >= 0) return {index, variant};
    }
  }
  return null;
}
function syncJSONValue(item) {
  if (!jsonModel || jsonModel.isDisposed()) return;
  const pretty = JSON.stringify(item, null, 2) + '\n';
  const current = jsonModel.getValue();
  let same = current === pretty;
  if (!same) {
    try { same = canonicalJSON(current) === canonicalJSON(pretty); } catch { same = false; }
  }
  if (same) return;
  jsonApplying = true;
  try { jsonModel.setValue(pretty); } finally { jsonApplying = false; }
  jsonAppliedText = pretty;
  jsonInvalid = false;
}
// Typing reaches the model through the native input pipeline, which can apply
// the tail of an input burst to the model without emitting further change
// events, so change events alone are not a reliable signal that the content
// has settled. The draft therefore converges to the editor through a periodic
// poll that pushes the editor text into the draft slot only when it differs
// from the last text this editor applied (jsonAppliedText keeps the direction
// one-way: editor -> draft). flushJSONToDraft runs the same propagation
// synchronously before anything can change which model slot is open.
function onJSONContent() {
  if (jsonApplying) return;
  clearTimeout(jsonApplyTimer);
  jsonApplyTimer = setTimeout(applyJSONToDraft, 180);
}
function applyJSONToDraft() {
  clearTimeout(jsonApplyTimer);
  jsonApplyTimer = null;
  if (jsonApplying || !jsonModel || jsonModel.isDisposed() || !editJSON || tab !== 'models') return;
  const editorText = jsonModel.getValue();
  if (editorText === jsonAppliedText) return;
  const doc = parsed();
  const slot = currentModelSlot(doc);
  if (!slot) return;
  let value;
  try { value = JSON.parse(editorText); } catch { jsonInvalid = true; return; }
  if (!object(value)) { jsonInvalid = true; return; }
  jsonInvalid = false;
  jsonAppliedText = editorText;
  jsonApplying = true;
  try {
    slot.list[slot.index] = value;
    changeObject(doc);
    renderList(doc);
  } finally { jsonApplying = false; }
}
function flushJSONToDraft() {
  if (jsonApplyTimer) applyJSONToDraft();
}
function openJSONEditor(doc, item, kind) {
  loadMonaco().then(monaco => {
    const slot = currentModelSlot(doc);
    if (!slot || tab !== 'models' || !editJSON || !object(item)) return;
    return loadModelSchema(monaco).then(() => {
      if (!jsonEditor) {
        jsonEditor = monaco.editor.create($('json-editor'), {
          // Monaco's new native EditContext input pipeline can leave the text
          // buffer inconsistent with the model's lines while typing; the
          // classic textarea pipeline keeps getValue() reliable.
          editContext: false,
          automaticLayout: true,
          minimap: {enabled: false},
          scrollBeyondLastLine: false,
          wordWrap: 'on',
          fontSize: 13,
          tabSize: 2,
          renderLineHighlight: 'none',
        });
      }
      const key = kind + ':' + selected + ':' + selectedVariant;
      if (jsonModelKey !== key || !jsonModel || jsonModel.isDisposed() || jsonModel.uri.toString() !== modelEditURIPrefix + kind) {
        jsonApplying = true;
        try {
          jsonModel?.dispose();
          jsonModel = monaco.editor.createModel(JSON.stringify(item, null, 2) + '\n', 'json', monaco.Uri.parse(modelEditURIPrefix + kind));
          jsonAppliedText = jsonModel.getValue();
          jsonModel.onDidChangeContent(onJSONContent);
          jsonEditor.setModel(jsonModel);
        } finally { jsonApplying = false; }
        jsonModelKey = key;
        jsonInvalid = false;
      } else {
        syncJSONValue(item);
      }
      if (!jsonPoll) jsonPoll = setInterval(applyJSONToDraft, 400);
      jsonEditor.layout();
    });
  }).catch(err => { message(err.message || 'Could not load the JSON editor.'); });
}
function renderPreview() {
  const preview = report?.previews?.[previewRunner];
  const name = previewRunner === 'dsh' ? 'DSH' : previewRunner === 'codex' ? 'Codex' : 'Grok';
  $('preview-title').textContent = name + (previewRunner === 'dsh' ? ' YAML export' : ' TOML snippet');
  $('merge-native-wrap').hidden = previewRunner !== 'codex';
  $('preview-target').textContent = previewRunner === 'dsh' ? 'Merge into DSH settings.' : previewRunner === 'codex' ? 'Merge into ~/.codex/config.toml; this is not a complete replacement file. The .json file is the Codex model catalog for the /model picker; select it and use Update to install it.' : `Merge into ~/.${previewRunner}/config.toml; this is not a complete replacement file.`;
  document.querySelectorAll('[data-runner]').forEach(el => el.setAttribute('aria-pressed', String(el.dataset.runner === previewRunner)));
  const files = previewFiles();
  previewFile = previewFileIndex(files, previewFileName);
  const nav = $('preview-files');
  nav.hidden = files.length < 2;
  nav.replaceChildren(...files.map((file, index) => {
    const el = button(previewFileLabel(file), () => { previewFileName = file.name; state(); });
    el.setAttribute('aria-pressed', String(index === Math.min(previewFile, files.length - 1)));
    return el;
  }));
  const file = selectedPreviewFile();
  const compare = showCatalogDiff(preview, file);
  const status = catalogStatus(file);
  $('preview-status').hidden = !status;
  $('preview-status').textContent = status;
  $('preview-content').textContent = preview?.error || file?.content || (report ? 'Preview unavailable. Check diagnostics below.' : 'Validate the current draft to preview it.');
  $('preview-content').hidden = compare;
  $('preview-diff').hidden = !compare;
  const token = ++diffToken;
  if (compare) {
    loadMonaco().then(monaco => {
      if (token !== diffToken) return;
      const current = selectedPreviewFile();
      if (!showCatalogDiff(report?.previews?.[previewRunner], current)) return;
      ensureCatalogDiff(monaco, current);
    }).catch(err => {
      if (token !== diffToken) return;
      $('preview-diff').hidden = true;
      $('preview-content').hidden = false;
      $('preview-status').hidden = false;
      $('preview-status').textContent = err.message || 'Could not load the diff editor.';
    });
  }
  renderCodexConfigWarnings(preview);
  updateCatalogInstall();
}
function renderCodexConfigWarnings(preview) {
  const list = $('codex-config-warnings');
  const warnings = previewRunner === 'codex' && Array.isArray(preview?.warnings) ? preview.warnings.slice(0, 2) : [];
  list.hidden = warnings.length === 0;
  list.replaceChildren(...warnings.map(warning => {
    const message = warning && warning.message ? warning.message : String(warning || '');
    return node('li', message, 'warning');
  }));
}
function updateCatalogInstall() {
  const install = $('catalog-install');
  const file = selectedPreviewFile();
  const show = previewRunner === 'codex' && file?.name === 'llm-proxy-codex.json' && !!file?.targetState;
  install.hidden = !show;
  if (!show) return;
  install.textContent = file.targetState === 'missing' ? 'Create' : file.targetState === 'same' ? 'Updated' : 'Update';
  install.disabled = file.targetState === 'same';
}
document.querySelectorAll('[data-runner]').forEach(el => el.onclick = () => { previewRunner = el.dataset.runner; previewFileName = ''; state(); });
$('merge-native').checked = mergeNativeEnabled();
$('merge-native').onchange = () => {
  try { localStorage.setItem(mergeNativeStorageKey, $('merge-native').checked ? '1' : '0'); } catch {}
  validate();
};
function showReport(value) {
  report = value;
  $('diagnostics').replaceChildren();
  const all = [...value.diagnostics, ...[...fieldErrors.values()].map(message => ({severity: 'error', path: 'form', message}))];
  $('diagnostic-count').textContent = all.length ? `· ${all.length}` : '';
  for (const d of all) $('diagnostics').append(node('li', `${d.severity === 'error' ? 'Error' : 'Warning'}: ${d.path}: ${d.message}`, d.severity));
  if (!all.length) $('diagnostics').append(node('li', 'Config valid.'));
  state();
}
async function validate() {
  clearTimeout(timer);
  const requestGeneration = ++generation;
  try {
    const value = await api('validate', {text, mergeNative: mergeNativeEnabled()});
    if (requestGeneration === generation) showReport(value);
  } catch (err) { if (requestGeneration === generation) { report = null; state(); message(err.message); } }
}
function changed() {
  generation++;
  report = null;
  state();
  clearTimeout(timer);
  timer = setTimeout(validate, 250);
}
function changeObject(doc) { text = JSON.stringify(doc, null, 2) + '\n'; $('raw').value = text; changed(); }
function parsed() { try { const doc = JSON.parse(text); return object(doc) ? doc : null; } catch { return null; } }
function mayDiscardFields() {
  if (!fieldErrors.size) return true;
  if (!confirm('Discard incomplete form fields? The last valid values remain in the draft.')) return false;
  fieldErrors.clear();
  return true;
}
async function load() {
  if ((text !== saved || fieldErrors.size) && !confirm('Reload from disk and discard unsaved changes?')) return;
  clearTimeout(jsonApplyTimer); jsonApplyTimer = null;
  try {
    const value = await api(`config?mergeNative=${mergeNativeEnabled() ? 'true' : 'false'}`);
    const urlState = parseEditorURL(location.search);
    clearTimeout(timer); generation++; fieldErrors.clear();
    text = saved = value.text; revision = value.revision;
    $('path').textContent = value.path;
    $('raw').value = text;
    selected = 0;
    selectedVariant = selectedProvider = -1;
    collapsedProviders.clear(); expandedModels.clear();
    const doc = parsed();
    if (!doc) tab = 'raw';
    else {
      // A ?model= deep link selects that base model or variant and expands its
      // branch; a name that is no longer in the file keeps the first model and
      // syncURL rewrites the address bar to it.
      const entry = findRoute(doc, urlState.model);
      if (entry) {
        selected = entry.index;
        selectedVariant = entry.variant;
        expandedModels.add(entry.index);
        const group = modelGroups(doc).find(g => g.models.some(m => m.index === entry.index));
        if (group) collapsedProviders.delete(group.key);
      }
      editJSON = urlState.edit === 'json';
    }
    render(); showReport(value.report); message('');
  } catch (err) { message(err.message); }
}
async function save() {
  if (busy || fieldErrors.size) return;
  busy = true; state();
  const submitted = text;
  try {
    const value = await api('save', {text: submitted, revision, mergeNative: mergeNativeEnabled()});
    saved = submitted; revision = value.revision;
    if (text === submitted) showReport(value.report);
    message(value.backup ? `Saved. Backup: ${value.backup}\nRestart the proxy separately to apply changes.` : 'No file changes.');
  } catch (err) { message(err.message); }
  finally { busy = false; state(); }
}
function field(parent, doc, target, key, label, kind = 'text', choices = null) {
  const wrap = node('label', undefined, `field${kind === 'json' ? ' wide' : ''}`);
  wrap.append(node('span', label));
  let input;
  if (choices || kind === 'bool') {
    input = node('select');
    const values = kind === 'bool' ? [['', 'Omitted / inherit'], ['null', 'Null'], ['true', 'Yes'], ['false', 'No']] : [['', 'Omitted / inherit'], ...choices.map(v => [v, v])];
    const current = target[key] === undefined ? '' : String(target[key]);
    if (!values.some(([v]) => v === current)) values.push([current, current]);
    for (const [value, title] of values) { const option = node('option', title); option.value = value; input.append(option); }
    input.value = current;
  } else {
    input = node(kind === 'json' ? 'textarea' : 'input');
    input.value = target[key] === undefined ? '' : kind === 'json' ? JSON.stringify(target[key], null, 2) : String(target[key]);
    if (kind === 'number') input.type = 'number';
    if (key === 'dummyToken') input.type = 'password';
    input.spellcheck = false;
  }
  input.onchange = () => {
    const errorKey = input;
    try {
      if (input.value === '') delete target[key];
      else if (kind === 'json' || kind === 'bool') target[key] = JSON.parse(input.value);
      else if (kind === 'number') target[key] = Number(input.value);
      else target[key] = input.value;
      fieldErrors.delete(errorKey);
      input.setCustomValidity('');
      input.removeAttribute('aria-invalid');
      changeObject(doc);
      if (['displayName', 'clientModelName', 'providerModelName', 'provider', 'name'].includes(key)) renderList(doc);
    } catch {
      fieldErrors.set(errorKey, `${label}: invalid JSON`);
      input.setCustomValidity('Enter valid JSON');
      input.setAttribute('aria-invalid', 'true');
      generation++; clearTimeout(timer); report = null;
      state(); message(`${label}: invalid JSON. Fix this field before saving.`);
    }
  };
  input.oninput = input.onchange;
  wrap.append(input); parent.append(wrap);
}
function inputsForm(parent, doc, target, variant) {
  const box = node('fieldset'); box.append(node('legend', 'Inputs'));
  const wrap = node('label', undefined, 'field'); wrap.append(node('span', 'Input mode'));
  const select = node('select');
  for (const [value, title] of [['omit', variant ? 'Inherit base inputs (omitted)' : 'Text + image (omitted)'], ['null', variant ? 'Inherit base inputs (null)' : 'Text + image (null)'], ['both', 'Text + image (empty list)'], ['custom', 'Explicit capabilities']]) {
    const opt = node('option', title); opt.value = value; select.append(opt);
  }
  select.value = target.inputs === undefined ? 'omit' : target.inputs === null ? 'null' : Array.isArray(target.inputs) && !target.inputs.length ? 'both' : 'custom';
  select.onchange = () => {
    if (!mayDiscardFields()) return;
    if (select.value === 'omit') delete target.inputs;
    else target.inputs = select.value === 'null' ? null : select.value === 'both' ? [] : [{type:'text'}, {type:'image'}];
    changeObject(doc); renderForm(doc);
  };
  wrap.append(select); box.append(wrap);
  if (select.value === 'custom') {
    const checks = node('div', undefined, 'checks');
    for (const type of ['text', 'image']) {
      const label = node('label'); const check = node('input'); check.type = 'checkbox';
      check.checked = Array.isArray(target.inputs) && target.inputs.some(i => object(i) && i.type === type && i.disabled !== true);
      check.onchange = () => {
        if (!Array.isArray(target.inputs)) target.inputs = [];
        const entry = target.inputs.find(i => object(i) && i.type === type);
        if (entry) entry.disabled = !check.checked;
        else target.inputs.push({type, disabled:!check.checked});
        changeObject(doc);
      };
      label.append(check, node('span', type)); checks.append(label);
    }
    box.append(checks);
  }
  parent.append(box);
}
function reasoningForm(parent, doc, target, variant) {
  const box = node('fieldset'); box.append(node('legend', 'Reasoning'));
  const label = node('label', undefined, 'field'); label.append(node('span', 'Reasoning mode'));
  const select = node('select');
  for (const [value, title] of [['omit', variant ? 'Inherit (omitted)' : 'Missing — required'], ['null', variant ? 'Inherit (null)' : 'Null — required'], ['disabled', 'Disabled'], ['enabled', 'Enabled']]) {
    const option = node('option', title); option.value = value; select.append(option);
  }
  select.value = target.reasoning === undefined ? 'omit' : target.reasoning === null ? 'null' : target.reasoning.disabled ? 'disabled' : 'enabled';
  select.onchange = () => {
    if (!mayDiscardFields()) return;
    if (select.value === 'omit') delete target.reasoning;
    else target.reasoning = select.value === 'null' ? null : select.value === 'disabled' ? {disabled:true} : {disabled:false, defaultEffort:'high', effortsMapping:{high:'high'}};
    changeObject(doc); renderForm(doc);
  };
  label.append(select); box.append(label);
  if (object(target.reasoning) && !target.reasoning.disabled) {
    const fields = node('div', undefined, 'fields');
    field(fields,doc,target.reasoning,'defaultEffort','Default effort','text',['low','medium','high','xhigh','max','ultra']);
    field(fields,doc,target.reasoning,'effortsMapping','Effort mappings (JSON)','json'); box.append(fields);
  }
  parent.append(box);
}
// modelProtocolOf resolves the upstream protocol a variant inherits from its
// base model (variant objects themselves carry no protocol field).
function modelProtocolOf(doc, variant) {
  const index = (doc.models || []).findIndex(m => Array.isArray(m.variants) && m.variants.includes(variant));
  const base = index >= 0 ? doc.models[index] : null;
  return object(base) ? base.protocol : '';
}

function modelFields(parent, doc, model, variant = false) {
  const fields = node('div', undefined, 'fields'); parent.append(fields);
  if (!variant) {
    field(fields,doc,model,'provider','Provider','text',(Array.isArray(doc.providers) ? doc.providers : []).filter(object).map(p=>p.name).filter(v=>typeof v==='string'));
    field(fields,doc,model,'protocol','Protocol','text',['openai-responses','anthropic-messages']);
    field(fields,doc,model,'providerModelName','Upstream model');
  }  field(fields,doc,model,'clientModelName','Client name');
  field(fields,doc,model,'displayName','Display name');
  if ((variant ? modelProtocolOf(doc, model) : model.protocol) === 'anthropic-messages') {
    field(fields,doc,model,'protocolAdapter','Protocol adapter (client surface)','text',['anthropic2openai']);
  }
  if (variant) field(fields,doc,model,'agentRunners','Agent runners (JSON; empty means all)','json');
  field(fields,doc,model,'contextWindow','Context window','number');
  field(fields,doc,model,'maxTokens','Max output tokens','number');
  inputsForm(parent,doc,model,variant); reasoningForm(parent,doc,model,variant);
  const advanced = node('details'); advanced.append(node('summary','Advanced transformations and compatibility'));
  const extra = node('div',undefined,'fields'); advanced.append(extra);
  for (const [key,label] of [['noImage','Strip images'],['adjustUsageForDSH','Adjust DSH usage'],['feedToGrokCli','Grok CLI stream filtering']]) field(extra,doc,model,key,label,'bool');
  field(extra,doc,model,'compat','Compatibility (JSON)','json');
  field(extra,doc,model,'effortMapping','Upstream effort mapping (JSON)','json'); parent.append(advanced);
}
function selectModel(index, variant = -1) {
  if (!mayDiscardFields()) return;
  flushJSONToDraft();
  selected = index; selectedVariant = variant; selectedProvider = -1;
  if (variant >= 0 && !$('search').value.trim()) expandedModels.add(index);
  render(); state();
}
function selectProvider(index) {
  if (!mayDiscardFields()) return;
  flushJSONToDraft();
  selectedProvider = index; selectedVariant = -1;
  if (tab === 'providers') selected = index;
  render(); state();
}
function addModel(doc, providerName = '') {
  if (!mayDiscardFields()) return;
  flushJSONToDraft();
  if (!Array.isArray(doc.models)) doc.models = [];
  const provider = (Array.isArray(doc.providers) ? doc.providers : []).find(p => object(p) && p.name === providerName);
  const protocol = provider?.kind === 'commandcode' ? 'anthropic-messages' : 'openai-responses';
  doc.models.push({provider:providerName, protocol, providerModelName:'new-model', reasoning:{disabled:true}});
  selected = doc.models.length - 1; selectedVariant = selectedProvider = -1;
  $('search').value = '';
  const group = modelGroups(doc).find(g => g.name === providerName);
  if (group) collapsedProviders.delete(group.key);
  changeObject(doc); render();
}
function addVariant(doc, base) {
  if (!mayDiscardFields()) return;
  flushJSONToDraft();
  if (!Array.isArray(base.variants)) base.variants = [];
  base.variants.push({clientModelName:(base.clientModelName || base.providerModelName || 'model') + '-variant'});
  selectedVariant = base.variants.length - 1;
  expandedModels.add(selected); $('search').value = '';
  changeObject(doc); render();
}
function modeToggle() {
  const wrap = node('div', undefined, 'mode-toggle');
  wrap.setAttribute('role', 'group');
  wrap.setAttribute('aria-label', 'Editor mode');
  const formButton = button('Form', () => {
    if (!editJSON) return;
    flushJSONToDraft();
    if (jsonInvalid) { message('Fix the JSON errors before returning to the form. The draft still holds the last valid model.'); return; }
    editJSON = false; render(); state();
  });
  const jsonButton = button('JSON', () => {
    if (editJSON || !mayDiscardFields()) return;
    editJSON = true; render(); state();
  });
  formButton.setAttribute('aria-pressed', String(!editJSON));
  jsonButton.setAttribute('aria-pressed', String(editJSON));
  wrap.append(formButton, jsonButton);
  return wrap;
}function renderForm(doc) {
  const form = $('form'); form.replaceChildren();
  $('json-editor').hidden = true;
  if (!doc) { form.append(node('p','Use Raw JSON to repair the config before editing forms.')); return; }
  const providerView = tab === 'providers' || selectedProvider >= 0;
  const providerIndex = tab === 'providers' ? selected : selectedProvider;
  const base = doc.models?.[selected];
  const variantView = !providerView && selectedVariant >= 0;
  const list = providerView ? doc.providers : variantView ? base?.variants : doc.models;
  const index = providerView ? providerIndex : variantView ? selectedVariant : selected;
  const item = Array.isArray(list) ? list[index] : null;
  if (!object(item)) { form.append(node('p','Select an entry, or use Raw JSON to repair an invalid entry.')); return; }
  if (!providerView) {
    const crumb = node('div', undefined, 'breadcrumb');
    const provider = (Array.isArray(doc.providers) ? doc.providers : []).findIndex(p => object(p) && p.name === base.provider);
    crumb.append(provider >= 0 ? button(base.provider, () => selectProvider(provider)) : node('span',base.provider || 'Missing provider'));
    if (variantView) { crumb.append(node('span',' / '), button(modelTitle(base), () => selectModel(selected)), node('span',' / Variant')); }
    else crumb.append(node('span',' / Base model'));
    form.append(crumb);
  }
  const heading = node('div',undefined,'actions');
  heading.append(node('h2', providerView ? item.name || 'New provider' : modelTitle(item, variantView ? 'Unnamed variant' : 'New model')));
  if (!providerView) heading.append(modeToggle());
  heading.append(button(variantView ? 'Duplicate variant' : 'Duplicate',()=>{
    if(!mayDiscardFields())return;
    flushJSONToDraft();
    list.push(structuredClone(item));
    if (providerView) { if(tab === 'providers') selected = list.length-1; else selectedProvider = list.length-1; }
    else if (variantView) selectedVariant = list.length-1;
    else { selected = list.length-1; selectedVariant = -1; }
    changeObject(doc); render();
  }));
  heading.append(button(variantView ? 'Delete variant' : 'Delete',()=>{
    if(!confirm(variantView ? 'Delete this variant from the draft?' : 'Delete this entry from the draft?') || !mayDiscardFields())return;
    flushJSONToDraft();
    list.splice(index,1);
    if (providerView) { selected = Math.max(0,index-1); selectedProvider = tab === 'models' && list.length ? selected : -1; }
    else if (variantView) selectedVariant = -1;
    else { selected = Math.max(0,index-1); expandedModels.clear(); }
    changeObject(doc); render();
  },'danger'));
  form.append(heading);
  if (!providerView && editJSON) {
    // JSON mode replaces the field grid with one editor over the whole model
    // (or variant) object; the variant links are inside the JSON itself.
    $('json-editor').hidden = false;
    openJSONEditor(doc, item, variantView ? 'variant' : 'model');
    return;
  }
  if (providerView) {
    const fields=node('div',undefined,'fields');form.append(fields);
    field(fields,doc,item,'name','Provider name');field(fields,doc,item,'kind','Kind','text',['codex','commandcode','grok','http-proxy']);
    field(fields,doc,item,'subscription','Subscription','bool');
    for(const [key,label] of [['baseUrl','Base URL'],['home','Home directory'],['authFile','Auth file path'],['version','Client version'],['dummyToken','Dummy token']]) field(fields,doc,item,key,label);
    form.append(node('p','Auth file paths are editable; this editor never reads their contents.','hint'));
  } else {
    if (variantView) form.append(node('p','Omitted fields inherit from the base model.','hint'));
    modelFields(form,doc,item,variantView);
    if (!variantView) {
      const section=node('fieldset');section.append(node('legend','Variants'));
      if(Array.isArray(base.variants)) base.variants.forEach((variant,i)=>{
        section.append(button(modelTitle(variant,`Variant ${i+1}`),()=>selectModel(selected,i),'variant-link'));
      });
      section.append(button('+ Add variant',()=>addVariant(doc,base)));form.append(section);
    }
  }
}
function renderList(doc) {
  const entries = $('entries'); entries.replaceChildren();
  entries.setAttribute('aria-label',tab === 'models' ? 'Models by provider' : 'Providers');
  if (tab === 'providers') {
    (Array.isArray(doc?.providers) ? doc.providers : []).forEach((item,index)=>{
      const title = object(item) ? item.name || `Provider ${index+1}` : 'Invalid provider';
      if(!title.toLowerCase().includes($('search').value.toLowerCase()))return;
      const el=button(title,()=>selectProvider(index),'entry');
      el.setAttribute('aria-pressed',String(selected===index)); entries.append(el);
    });
    return;
  }
  const searching = Boolean($('search').value.trim());
  const groups = filterModelGroups(modelGroups(doc),$('search').value);
  const tree = node('ul',undefined,'model-tree'); entries.append(tree);
  for (const group of groups) {
    const branch = node('li'); branch.dataset.level = 'provider';
    const row = node('div',undefined,'tree-row');
    const children = node('ul');
    const open = searching || !collapsedProviders.has(group.key);
    const toggle = button(open ? '▾' : '▸',()=>{
      if (searching) return;
      if (open) collapsedProviders.add(group.key); else collapsedProviders.delete(group.key);
      renderList(parsed());
    },'tree-toggle');
    toggle.setAttribute('aria-label',`${open ? 'Collapse' : 'Expand'} provider ${group.label}`);
    toggle.setAttribute('aria-expanded',String(open)); toggle.disabled = searching;
    const label = group.provider >= 0 ? button(group.label,()=>selectProvider(group.provider),'entry') : node('span',group.label,'entry warning');
    label.setAttribute('aria-pressed',String(selectedProvider===group.provider && group.provider>=0));
    row.append(toggle,label);
    const add=button('+',()=>addModel(parsed(),group.name),'tree-add');
    add.setAttribute('aria-label',`Add model to ${group.label}`);row.append(add);
    branch.append(row,children); children.hidden = !open;
    for (const entry of group.models) {
      const base = node('li');base.dataset.level='model';base.dataset.modelIndex=entry.index;
      const baseRow=node('div',undefined,'tree-row');
      const variants=node('ul');
      const expanded=searching || expandedModels.has(entry.index);
      if(entry.variants.length) {
        const expand=button(expanded?'▾':'▸',()=>{
          if(searching)return;
          if(expanded)expandedModels.delete(entry.index);else expandedModels.add(entry.index);
          renderList(parsed());
        },'tree-toggle');
        expand.setAttribute('aria-label',`${expanded?'Collapse':'Expand'} model ${modelTitle(entry.model)}`);
        expand.setAttribute('aria-expanded',String(expanded));expand.disabled=searching;baseRow.append(expand);
      }
      const baseButton=button(modelTitle(entry.model),()=>selectModel(entry.index),'entry');
      baseButton.setAttribute('aria-pressed',String(selectedProvider<0 && selected===entry.index && selectedVariant<0));
      baseRow.append(baseButton);base.append(baseRow,variants);variants.hidden=!expanded;
      for(const {variant,index} of entry.variants) {
        const leaf=node('li');leaf.dataset.level='variant';leaf.dataset.variantIndex=index;
        const variantButton=button(modelTitle(variant,`Variant ${index+1}`),()=>selectModel(entry.index,index),'entry');
        variantButton.setAttribute('aria-pressed',String(selectedProvider<0 && selected===entry.index && selectedVariant===index));
        leaf.append(variantButton);variants.append(leaf);
      }
      children.append(base);
    }
    if(!group.models.length)children.append(node('li','No models','hint'));
    tree.append(branch);
  }
  if(!groups.length)entries.append(node('p','No matching models.','hint'));
}
function render() {
  document.querySelectorAll('[data-tab]').forEach(el=>el.setAttribute('aria-pressed',String(el.dataset.tab===tab)));
  $('structured').hidden=!['models','providers'].includes(tab);
  $('raw-panel').hidden=tab!=='raw';$('preview-panel').hidden=tab!=='preview';
  if(tab==='models'||tab==='providers'){const doc=parsed();renderList(doc);renderForm(doc);}
  if(tab==='raw')$('raw').value=text;
}
document.querySelectorAll('[data-tab]').forEach(el=>el.onclick=()=>{if(!mayDiscardFields())return;flushJSONToDraft();tab=el.dataset.tab;selected=0;selectedVariant=selectedProvider=-1;$('search').value='';render();state();validate();});
$('add').onclick=()=>{
  if(!mayDiscardFields())return;flushJSONToDraft();const doc=parsed();if(!doc){message('Repair the JSON first.');return;}
  if(tab==='models'){addModel(doc,selectedProvider>=0?doc.providers?.[selectedProvider]?.name || '':doc.models?.[selected]?.provider || '');return;}
  if(!Array.isArray(doc[tab]))doc[tab]=[];
  doc[tab].push(tab==='providers'?{name:'new-provider',kind:'codex',subscription:true}:{provider:'',protocol:'openai-responses',providerModelName:'new-model',reasoning:{disabled:true}});
  selected=doc[tab].length-1;changeObject(doc);render();
};
$('search').oninput=()=>renderList(parsed());
$('raw').oninput=()=>{text=$('raw').value;selected=0;selectedVariant=selectedProvider=-1;expandedModels.clear();changed();};
$('reload').onclick=load;$('validate').onclick=validate;$('save').onclick=save;
$('copy').onclick=async()=>{try{await navigator.clipboard.writeText(selectedPreviewFile().content);message('Preview copied.');}catch(err){message(err.message);}};
$('catalog-install').onclick=async()=>{
  if (busy) return;
  busy = true; state();
  try {
    const value = await api('catalog', {text, mergeNative: mergeNativeEnabled()});
    if (value.result === 'unchanged') message('Already up to date.');
    else message((value.result === 'created' ? 'Created ' : 'Updated ') + value.path);
    await validate();
  } catch (err) { message(err.message); }
  finally { busy = false; state(); }
};
$('download').onclick=()=>{const file=selectedPreviewFile();const mime=file.format==='yaml'?'text/yaml':file.format==='json'?'application/json':'application/toml';const url=URL.createObjectURL(new Blob([file.content],{type:mime}));const link=node('a');link.href=url;link.download=file.name;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);};
window.addEventListener('beforeunload',event=>{if(text!==saved||fieldErrors.size){event.preventDefault();event.returnValue='';}});
load();
