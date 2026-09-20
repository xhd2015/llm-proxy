import {modelGroups, filterModelGroups, modelTitle} from './tree.mjs';

const $ = (id) => document.getElementById(id);
let text = '', saved = '', revision = '', tab = 'models', selected = 0, report = null;
let previewRunner = 'dsh';
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
function renderPreview() {
  const preview = report?.previews?.[previewRunner];
  const name = previewRunner === 'dsh' ? 'DSH' : previewRunner === 'codex' ? 'Codex' : 'Grok';
  $('preview-title').textContent = name + (previewRunner === 'dsh' ? ' YAML export' : ' TOML snippet');
  $('preview-target').textContent = previewRunner === 'dsh' ? 'Merge into DSH settings.' : `Merge into ~/.${previewRunner}/config.toml; this is not a complete replacement file.`;
  document.querySelectorAll('[data-runner]').forEach(el => el.setAttribute('aria-pressed', String(el.dataset.runner === previewRunner)));
  $('preview-content').textContent = preview?.error || preview?.content || (report ? 'Preview unavailable. Check diagnostics below.' : 'Validate the current draft to preview it.');
}
document.querySelectorAll('[data-runner]').forEach(el => el.onclick = () => { previewRunner = el.dataset.runner; state(); });
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
    const value = await api('validate', {text});
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
  try {
    const value = await api('config');
    clearTimeout(timer); generation++; fieldErrors.clear();
    text = saved = value.text; revision = value.revision;
    $('path').textContent = value.path;
    $('raw').value = text;
    selected = 0;
    selectedVariant = selectedProvider = -1;
    collapsedProviders.clear(); expandedModels.clear();
    if (!parsed()) tab = 'raw';
    render(); showReport(value.report); message('');
  } catch (err) { message(err.message); }
}
async function save() {
  if (busy || fieldErrors.size) return;
  busy = true; state();
  const submitted = text;
  try {
    const value = await api('save', {text: submitted, revision});
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
function modelFields(parent, doc, model, variant = false) {
  const fields = node('div', undefined, 'fields'); parent.append(fields);
  if (!variant) {
    field(fields,doc,model,'provider','Provider','text',(Array.isArray(doc.providers) ? doc.providers : []).filter(object).map(p=>p.name).filter(v=>typeof v==='string'));
    field(fields,doc,model,'protocol','Protocol','text',['openai-responses','anthropic-messages']);
    field(fields,doc,model,'providerModelName','Upstream model');
  }
  field(fields,doc,model,'clientModelName','Client name');
  field(fields,doc,model,'displayName','Display name');
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
  selected = index; selectedVariant = variant; selectedProvider = -1;
  if (variant >= 0 && !$('search').value.trim()) expandedModels.add(index);
  render();
}
function selectProvider(index) {
  if (!mayDiscardFields()) return;
  selectedProvider = index; selectedVariant = -1;
  if (tab === 'providers') selected = index;
  render();
}
function addModel(doc, providerName = '') {
  if (!mayDiscardFields()) return;
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
  if (!Array.isArray(base.variants)) base.variants = [];
  base.variants.push({clientModelName:(base.clientModelName || base.providerModelName || 'model') + '-variant'});
  selectedVariant = base.variants.length - 1;
  expandedModels.add(selected); $('search').value = '';
  changeObject(doc); render();
}
function renderForm(doc) {
  const form = $('form'); form.replaceChildren();
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
  heading.append(button(variantView ? 'Duplicate variant' : 'Duplicate',()=>{
    if(!mayDiscardFields())return;
    list.push(structuredClone(item));
    if (providerView) { if(tab === 'providers') selected = list.length-1; else selectedProvider = list.length-1; }
    else if (variantView) selectedVariant = list.length-1;
    else { selected = list.length-1; selectedVariant = -1; }
    changeObject(doc); render();
  }));
  heading.append(button(variantView ? 'Delete variant' : 'Delete',()=>{
    if(!confirm(variantView ? 'Delete this variant from the draft?' : 'Delete this entry from the draft?') || !mayDiscardFields())return;
    list.splice(index,1);
    if (providerView) { selected = Math.max(0,index-1); selectedProvider = tab === 'models' && list.length ? selected : -1; }
    else if (variantView) selectedVariant = -1;
    else { selected = Math.max(0,index-1); expandedModels.clear(); }
    changeObject(doc); render();
  },'danger'));
  form.append(heading);
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
document.querySelectorAll('[data-tab]').forEach(el=>el.onclick=()=>{if(!mayDiscardFields())return;tab=el.dataset.tab;selected=0;selectedVariant=selectedProvider=-1;$('search').value='';render();validate();});
$('add').onclick=()=>{
  if(!mayDiscardFields())return;const doc=parsed();if(!doc){message('Repair the JSON first.');return;}
  if(tab==='models'){addModel(doc,selectedProvider>=0?doc.providers?.[selectedProvider]?.name || '':doc.models?.[selected]?.provider || '');return;}
  if(!Array.isArray(doc[tab]))doc[tab]=[];
  doc[tab].push(tab==='providers'?{name:'new-provider',kind:'codex',subscription:true}:{provider:'',protocol:'openai-responses',providerModelName:'new-model',reasoning:{disabled:true}});
  selected=doc[tab].length-1;changeObject(doc);render();
};
$('search').oninput=()=>renderList(parsed());
$('raw').oninput=()=>{text=$('raw').value;selected=0;selectedVariant=selectedProvider=-1;expandedModels.clear();changed();};
$('reload').onclick=load;$('validate').onclick=validate;$('save').onclick=save;
$('copy').onclick=async()=>{try{await navigator.clipboard.writeText(report.previews[previewRunner].content);message('Preview copied.');}catch(err){message(err.message);}};
$('download').onclick=()=>{const url=URL.createObjectURL(new Blob([report.previews[previewRunner].content],{type:previewRunner==='dsh'?'text/yaml':'application/toml'}));const link=node('a');link.href=url;link.download=`llm-proxy-${previewRunner}.${report.previews[previewRunner].format}`;link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);};
window.addEventListener('beforeunload',event=>{if(text!==saved||fieldErrors.size){event.preventDefault();event.returnValue='';}});
load();
