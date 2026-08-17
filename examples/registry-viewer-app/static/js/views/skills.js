/** Skills control-plane view. Bodies are shown only in this operator surface. */
import { fetchAPI, requestJSON, invalidateCache } from '../api.js';
import { escapeHtml, formatDateTime, syntaxHighlightJson } from '../utils/format.js';
import { showLoading, hideLoading } from '../utils/dom.js';

const EMPTY_PACKAGE = {
    display_name: '', description: '', domains: [], tags: [],
    planning_instructions: [], response_instructions: [], tool_hints: [], resources: [],
    activation_examples: { should_activate: [], should_not_activate: [] },
    change_reason: '',
};

let skills = [];
let selected = null;
let activeTab = 'skills-package';
let draftMode = false;
let packageEditMode = false;
let currentETag = '';
let versions = [];

function skillID(skill) { return `${skill.ref.namespace}/${skill.ref.name}`; }

function renderPills(values, kind = 'domain') {
    const normalized = (values || []).filter(Boolean);
    if (!normalized.length) return '<span class="skill-empty-value">—</span>';
    return `<span class="skill-pill-list">${normalized.map(value =>
        `<span class="skill-pill skill-pill-${kind}">${escapeHtml(value)}</span>`
    ).join('')}</span>`;
}

function renderHighlightedJSON(value) {
    return `<pre class="json-view skill-json-view">${syntaxHighlightJson(value)}</pre>`;
}

function renderSkillSummary(identity, pkg, version = 0) {
    const exactRef = identity.namespace
        ? `${identity.namespace}/${identity.name}${version ? `@v${version}` : ''}`
        : 'Identity will be assigned from the fields below';
    return `<section class="glass-card skill-summary-card">
        <div class="skill-summary-heading">
            <div>
                <span class="skill-eyebrow">${version ? 'Published skill' : 'Skill package draft'}</span>
                <h3>${escapeHtml(pkg.display_name || identity.name || 'New skill')}</h3>
                <code class="skill-exact-ref">${escapeHtml(exactRef)}</code>
            </div>
            ${version ? `<span class="skill-status-badge success">Published · v${version}</span>` : '<span class="skill-status-badge pending">Draft</span>'}
        </div>
        <p class="skill-summary-description">${escapeHtml(pkg.description || 'Add a concrete description so developers and runtime selectors know when this skill applies.')}</p>
        <div class="skill-summary-taxonomy">
            <div><span class="skill-meta-label">Domains</span>${renderPills(pkg.domains, 'domain')}</div>
            <div><span class="skill-meta-label">Tags</span>${renderPills(pkg.tags, 'tag')}</div>
        </div>
    </section>`;
}

function showError(error) {
    document.getElementById('skillsErrorMessage').textContent = error?.message || String(error);
    document.getElementById('skillsErrorBanner').classList.add('visible');
}

function clearError() { document.getElementById('skillsErrorBanner').classList.remove('visible'); }

async function fetchSkills() {
    const panel = document.getElementById('skillsListPanel');
    showLoading(panel, 'Loading skills...');
    try {
        invalidateCache('/api/v1/skills');
        const { data } = await fetchAPI('/api/v1/skills');
        skills = (data.skills || []).sort((a, b) => skillID(a).localeCompare(skillID(b)));
        document.getElementById('skillsCount').textContent = skills.length;
        document.getElementById('skillsLastUpdated').textContent = `Last updated: ${formatDateTime(new Date())}`;
        renderList();
        clearError();
    } catch (error) {
        showError(error);
    } finally {
        hideLoading(panel);
    }
}

function renderList() {
    const search = document.getElementById('skillsSearchInput').value.trim().toLowerCase();
    const filtered = skills.filter(skill => !search || [
        skillID(skill), skill.display_name, skill.description,
        ...(skill.domains || []), ...(skill.tags || []),
    ].some(value => String(value || '').toLowerCase().includes(search)));
    const body = document.getElementById('skillsTableBody');
    body.innerHTML = filtered.length ? filtered.map(skill => `
        <tr data-skill-id="${escapeHtml(skillID(skill))}" class="${selected && skillID(selected.revision.metadata) === skillID(skill) ? 'selected' : ''}">
            <td>
                <span class="service-name skill-list-title">${escapeHtml(skill.display_name || skill.ref.name)}</span>
                <span class="skill-list-ref">${escapeHtml(skillID(skill))}</span>
                <span class="skill-list-description">${escapeHtml(skill.description || 'No description')}</span>
            </td>
            <td>${renderPills(skill.domains, 'domain')}</td>
            <td>${renderPills(skill.tags, 'tag')}</td>
            <td><span class="skill-version-badge">v${skill.published_version}</span></td>
        </tr>`).join('') : `<tr><td colspan="4" class="skills-empty">No published skills found</td></tr>`;
}

async function selectSkill(id) {
    const [namespace, name] = id.split('/');
    try {
        const response = await requestJSON(`/api/v1/skills/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`);
        selected = response.data;
        currentETag = response.etag;
        draftMode = false;
        packageEditMode = false;
        activeTab = 'skills-package';
        const versionResponse = await requestJSON(`/api/v1/skills/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/versions`);
        versions = versionResponse.data.versions || [];
        renderList();
        renderDetail();
        clearError();
    } catch (error) { showError(error); }
}

function startDraft() {
    selected = null;
    currentETag = '';
    versions = [];
    draftMode = true;
    packageEditMode = true;
    activeTab = 'skills-package';
    renderDetail();
}

function renderDetail() {
    const title = document.getElementById('skillsDetailTitle');
    const content = document.getElementById('skillsDetailContent');
    document.querySelectorAll('#skillsDetailPanel .detail-tab').forEach(button => {
        button.classList.toggle('active', button.dataset.tab === activeTab);
    });
    if (!selected && !draftMode) {
        title.textContent = 'Select a skill';
        content.innerHTML = `<div class="empty-detail"><div class="empty-detail-icon">🧩</div><div>Select a skill or create a new one</div></div>`;
        return;
    }
    const identity = selected?.revision?.metadata?.ref || { namespace: '', name: '' };
    title.textContent = identity.namespace ? `${identity.namespace}/${identity.name}` : 'New skill';
    if (activeTab === 'skills-versions') {
        content.innerHTML = renderVersions(identity);
    } else if (activeTab === 'skills-json') {
        content.innerHTML = `<section class="glass-container skill-json-card">
            <div class="skill-card-heading"><div><span class="skill-eyebrow">Complete representation</span><h3>Published package JSON</h3></div></div>
            <div class="json-container">${renderHighlightedJSON(selected || EMPTY_PACKAGE)}</div>
        </section>`;
    } else {
        content.innerHTML = renderPackageForm(identity, selected?.package || EMPTY_PACKAGE);
    }
}

function renderPackageForm(identity, pkg) {
    const version = selected?.revision?.ref?.version || 0;
    const editing = draftMode || packageEditMode;
    const packageContent = editing ? `<form id="skillPackageForm" class="skill-form">
        <div class="skill-identity-row">
            <label>Namespace<input name="namespace" required pattern="[A-Za-z0-9][A-Za-z0-9._-]*" value="${escapeHtml(identity.namespace)}" ${selected ? 'readonly' : ''}></label>
            <label>Name<input name="name" required pattern="[A-Za-z0-9][A-Za-z0-9._-]*" value="${escapeHtml(identity.name)}" ${selected ? 'readonly' : ''}></label>
        </div>
        <label>Complete package JSON<textarea name="package" spellcheck="false" aria-describedby="skillPackageHint">${escapeHtml(JSON.stringify(pkg, null, 2))}</textarea></label>
        <span id="skillPackageHint" class="skill-editor-hint">Validate before publishing. The editor contains authoring bodies; execution debug records do not.</span>
        <div class="skill-form-actions">
            ${selected ? '<button class="skill-action-secondary" type="button" data-skill-action="cancel-edit">Cancel</button>' : ''}
            <button class="skill-action-secondary" type="button" data-skill-action="validate">Validate</button>
            <button class="skill-action-secondary" type="button" data-skill-action="analyze">Analyze</button>
            <button class="skill-action-primary" type="submit">${selected ? 'Publish update' : 'Publish first version'}</button>
        </div>
        <div id="skillFormResult" class="skill-form-result">${currentETag ? `Current ETag: <code>${escapeHtml(currentETag)}</code>` : 'Validation is deterministic and never stores the draft. Analysis is available only when the host configures an advisor.'}</div>
    </form>` : `<div class="skill-package-display">
        <div class="json-container">${renderHighlightedJSON(pkg)}</div>
    </div>`;
    return `<div class="skill-view-stack">
        ${renderSkillSummary(identity, pkg, version)}
        <section class="glass-container skill-authoring-card">
            <div class="skill-card-heading">
                <div><span class="skill-eyebrow">${editing ? 'Authoring' : 'Published package'}</span><h3>Complete package</h3></div>
                <div class="skill-card-actions">
                    <span class="skill-card-hint">JSON · provider-neutral</span>
                    ${editing ? '' : '<button class="skill-action-secondary" type="button" data-skill-action="edit-package">Edit package</button>'}
                </div>
            </div>
            ${packageContent}
        </section>
    </div>`;
}

function renderVersions(identity) {
    if (!selected) return `<div class="empty-detail">Publish the first version before viewing history.</div>`;
    const currentVersion = selected.revision?.ref?.version;
    return `<div class="skill-view-stack">
        ${renderSkillSummary(identity, selected.package || EMPTY_PACKAGE, currentVersion)}
        <section class="glass-container skill-version-panel">
            <div class="skill-card-heading">
                <div><span class="skill-eyebrow">Immutable history</span><h3>Revision history</h3></div>
                <code class="skill-exact-ref">${escapeHtml(`${identity.namespace}/${identity.name}`)}</code>
            </div>
            <div class="skill-version-list">
                ${(versions || []).map(version => `<article class="skill-version-card ${version.status === 'deleted' ? 'skill-status-failed' : 'skill-status-success'}">
                    <div class="skill-version-heading">
                        <div><span class="skill-version-number">Version ${version.ref.version}</span><code>${escapeHtml(version.ref.manifest_hash || 'manifest not retained')}</code></div>
                        <span class="skill-status-badge ${version.status === 'deleted' ? 'failed' : 'success'}">${escapeHtml(version.status)}</span>
                    </div>
                    ${version.deleted_at ? `<p class="skill-version-note">Deleted ${escapeHtml(formatDateTime(version.deleted_at))}${version.reason ? ` · ${escapeHtml(version.reason)}` : ''}</p>` : ''}
                    ${version.status === 'retained' ? `<button class="skill-action-danger" type="button" data-delete-version="${version.ref.version}">Delete version</button>` : ''}
                </article>`).join('') || '<div class="empty-detail">No version history.</div>'}
            </div>
            <div class="skill-version-footer">
                <button class="skill-action-danger" type="button" data-delete-range>Delete version range</button>
                <p class="skill-safety-note">The published revision and published−1 are protected. Recovery is roll-forward by publishing a corrected package.</p>
            </div>
        </section>
    </div>`;
}

function formInput() {
    const form = document.getElementById('skillPackageForm');
    const namespace = form.elements.namespace.value.trim();
    const name = form.elements.name.value.trim();
    let pkg;
    try { pkg = JSON.parse(form.elements.package.value); }
    catch { throw new Error('Package JSON is invalid.'); }
    return { namespace, name, pkg };
}

async function validateDraft() {
    try {
        const { namespace, name, pkg } = formInput();
        const response = await requestJSON(`/api/v1/skills/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/validate`, { method: 'POST', body: pkg });
        document.getElementById('skillFormResult').innerHTML = `<div class="skill-result-heading">Validation result</div>${renderHighlightedJSON(response.data)}`;
        clearError();
    } catch (error) { showError(error); }
}

async function analyzeDraft() {
    try {
        const { namespace, name, pkg } = formInput();
        const response = await requestJSON(`/api/v1/skills/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/analyze`, { method: 'POST', body: pkg });
        document.getElementById('skillFormResult').innerHTML = `<div class="skill-result-heading">Authoring analysis</div>${renderHighlightedJSON(response.data)}`;
        clearError();
    } catch (error) {
        if (error.status === 404) {
            showError(new Error('Authoring analysis is not configured by this host. Deterministic validation remains available.'));
            return;
        }
        showError(error);
    }
}

async function publishDraft() {
    try {
        const { namespace, name, pkg } = formInput();
        const headers = currentETag ? { 'If-Match': currentETag } : { 'If-None-Match': '*' };
        headers['Idempotency-Key'] = globalThis.crypto?.randomUUID?.() || `viewer-${Date.now()}-${Math.random().toString(16).slice(2)}`;
        const response = await requestJSON(`/api/v1/skills/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`, { method: 'PUT', headers, body: pkg });
        currentETag = response.etag;
        await fetchSkills();
        await selectSkill(`${namespace}/${name}`);
    } catch (error) { showError(error); }
}

async function deleteVersionRange() {
    if (!selected || !currentETag) return;
    const ref = selected.revision.metadata.ref;
    const range = window.prompt('Version range to delete (for example, 1-3):');
    if (!range) return;
    const match = range.trim().match(/^(\d+)\s*-\s*(\d+)$/);
    if (!match) { showError(new Error('Enter a version range such as 1-3.')); return; }
    const from = Number(match[1]);
    const to = Number(match[2]);
    if (!Number.isSafeInteger(from) || !Number.isSafeInteger(to) || from < 1 || to < from) {
        showError(new Error('The deletion range must contain positive versions in ascending order.'));
        return;
    }
    const reason = window.prompt(`Why should versions ${from}-${to} be deleted?`);
    if (!reason) return;
    try {
        await requestJSON(`/api/v1/skills/${encodeURIComponent(ref.namespace)}/${encodeURIComponent(ref.name)}/versions?from=${from}&to=${to}`, {
            method: 'DELETE', headers: { 'If-Match': currentETag, 'X-Audit-Reason': reason },
        });
        await selectSkill(skillID(ref));
        await fetchSkills();
    } catch (error) { showError(error); }
}

async function deleteVersion(version) {
    if (!selected || !currentETag) return;
    const ref = selected.revision.metadata.ref;
    const reason = window.prompt(`Why should version ${version} be deleted?`);
    if (!reason) return;
    try {
        await requestJSON(`/api/v1/skills/${encodeURIComponent(ref.namespace)}/${encodeURIComponent(ref.name)}/versions/${version}`, {
            method: 'DELETE', headers: { 'If-Match': currentETag, 'X-Audit-Reason': reason },
        });
        await selectSkill(skillID(ref));
        await fetchSkills();
    } catch (error) { showError(error); }
}

function onListClick(event) {
    const row = event.target.closest('[data-skill-id]');
    if (row) selectSkill(row.dataset.skillId);
}
function onDetailClick(event) {
    const tab = event.target.closest('.detail-tab[data-tab]');
    if (tab) { activeTab = tab.dataset.tab; renderDetail(); return; }
    if (event.target.closest('[data-skill-action="edit-package"]')) {
        packageEditMode = true;
        renderDetail();
        return;
    }
    if (event.target.closest('[data-skill-action="cancel-edit"]')) {
        packageEditMode = false;
        renderDetail();
        return;
    }
    if (event.target.closest('[data-skill-action="validate"]')) validateDraft();
    if (event.target.closest('[data-skill-action="analyze"]')) analyzeDraft();
    const deletion = event.target.closest('[data-delete-version]');
    if (deletion) deleteVersion(deletion.dataset.deleteVersion);
    if (event.target.closest('[data-delete-range]')) deleteVersionRange();
}
function onSubmit(event) { if (event.target.id === 'skillPackageForm') { event.preventDefault(); publishDraft(); } }
function onSearch() { renderList(); }

export function init() {
    document.getElementById('skillsTableBody').addEventListener('click', onListClick);
    document.getElementById('skillsDetailPanel').addEventListener('click', onDetailClick);
    document.getElementById('skillsDetailPanel').addEventListener('submit', onSubmit);
    document.getElementById('skillsSearchInput').addEventListener('input', onSearch);
    document.getElementById('skillsNewButton').addEventListener('click', startDraft);
    fetchSkills();
}

export function destroy() {
    document.getElementById('skillsTableBody').removeEventListener('click', onListClick);
    document.getElementById('skillsDetailPanel').removeEventListener('click', onDetailClick);
    document.getElementById('skillsDetailPanel').removeEventListener('submit', onSubmit);
    document.getElementById('skillsSearchInput').removeEventListener('input', onSearch);
    document.getElementById('skillsNewButton').removeEventListener('click', startDraft);
}

export function refresh() { return fetchSkills(); }
