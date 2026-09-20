// Found Footy operator browser: fetch authoritative reads and apply SSE invalidations.
(() => {
  const state = { fixtures: [], total: 0, offset: 0, limit: 50, selected: null, expandedFixture: null, refreshTimer: null, detailDirty: false };
  const fixturesNode = document.querySelector('#fixtures');
  const detailNode = document.querySelector('#detail');
  const countNode = document.querySelector('#fixture-count');
  const moreNode = document.querySelector('#load-more');
  const connectionNode = document.querySelector('#connection');
  const refreshDetailNode = document.querySelector('#refresh-detail');

  function node(tag, attrs = {}, children = []) {
    const out = document.createElement(tag);
    for (const [key, value] of Object.entries(attrs)) {
      if (key === 'class') out.className = value;
      else if (key === 'text') out.textContent = value;
      else if (key.startsWith('on')) out.addEventListener(key.slice(2).toLowerCase(), value);
      else out.setAttribute(key, value);
    }
    for (const child of children) out.append(child);
    return out;
  }

  function clock(event) {
    return event.extra >= 0 ? `${event.minute}+${event.extra}′` : `${event.minute}′`;
  }

  function bytes(value) {
    if (!value) return '—';
    const units = ['B', 'KB', 'MB', 'GB'];
    const rank = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
    return `${(value / (1024 ** rank)).toFixed(rank > 1 ? 1 : 0)} ${units[rank]}`;
  }

  function duration(ms) { return `${(ms / 1000).toFixed(1)}s`; }

  function pill(text, kind = '') { return node('span', { class: `pill ${kind}`, text }); }

  function fixtureScore(fixture) {
    const home = fixture.home_score < 0 ? '–' : fixture.home_score;
    const away = fixture.away_score < 0 ? '–' : fixture.away_score;
    return `${home} : ${away}`;
  }

  function fixtureMeta(fixture) {
    const kickoff = new Date(fixture.kickoff).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
    return [fixture.league_name, fixture.league_round, kickoff].filter(Boolean).join(' · ');
  }

  function eventButton(event) {
    const title = event.player_name || event.detail || event.event_type;
    return node('button', {
      class: `fixture-event ${state.selected === event.id ? 'active' : ''}`,
      type: 'button',
      onClick: () => selectEvent(event.id).catch(showDetailError),
    }, [
      node('div', { class: 'event-title' }, [node('span', { text: title }), node('span', { text: clock(event) })]),
      node('div', { class: 'pill-row' }, [
        pill(`${event.asset_count} MD5`, event.asset_count ? 'good' : 'warn'),
        pill(`${event.playable_assets} playable`),
        pill(`${event.candidate_count} sources`),
        ...(event.removed ? [pill('removed', 'bad')] : []),
      ]),
    ]);
  }

  function renderFixtures() {
    fixturesNode.replaceChildren();
    countNode.textContent = `${state.total} fixtures · ${state.fixtures.length} loaded`;
    for (const fixture of state.fixtures) {
      const expanded = state.expandedFixture === fixture.id;
      const selectedHere = fixture.events.some(event => event.id === state.selected);
      const header = node('button', {
        class: `fixture-head ${selectedHere ? 'selected' : ''}`,
        type: 'button',
        'aria-expanded': String(expanded),
        onClick: () => {
          state.expandedFixture = expanded ? null : fixture.id;
          renderFixtures();
        },
      }, [
        node('div', { class: 'fixture-chevron', text: expanded ? '▾' : '▸' }),
        node('div', { class: 'fixture-main' }, [
          node('div', { class: 'fixture-teams' }, [
            node('span', { text: fixture.home_team }),
            node('span', { text: fixture.away_team }),
          ]),
          node('div', { class: 'fixture-meta', text: fixtureMeta(fixture) }),
          node('div', { class: 'pill-row' }, [
            pill(fixture.status_short || fixture.state, fixture.state === 'active' ? 'good' : ''),
            pill(`${fixture.event_count} events`),
            pill(`${fixture.asset_count} MD5`),
            pill(`${fixture.candidate_count} sources`),
          ]),
        ]),
        node('div', { class: 'fixture-score', text: fixtureScore(fixture) }),
      ]);
      const events = node('div', { class: `fixture-events ${expanded ? '' : 'hidden'}` });
      for (const event of fixture.events) events.append(eventButton(event));
      fixturesNode.append(node('article', { class: `fixture ${selectedHere ? 'active' : ''}` }, [header, events]));
    }
    moreNode.classList.toggle('hidden', state.fixtures.length >= state.total);
  }

  async function loadFixtures(reset = true) {
    if (reset) state.offset = 0;
    const params = new URLSearchParams({ limit: String(state.limit), offset: String(state.offset) });
    const query = document.querySelector('#query').value.trim();
    const eventType = document.querySelector('#event-type').value;
    if (query) params.set('q', query);
    if (eventType) params.set('type', eventType);
    if (document.querySelector('#with-assets').checked) params.set('with_assets', 'true');
    const response = await fetch(`/api/fixtures?${params}`);
    if (!response.ok) throw new Error(`fixtures ${response.status}`);
    const body = await response.json();
    state.fixtures = reset ? body.fixtures : state.fixtures.concat(body.fixtures);
    state.total = body.total;
    renderFixtures();
  }

  function stat(label, value) {
    return node('div', { class: 'stat' }, [node('b', { text: String(value) }), node('span', { text: label })]);
  }

  function renderAsset(asset, index) {
    const badges = [
      pill(asset.current_visible ? `public rank ${asset.current_rank}` : (asset.superseded_by ? 'superseded' : (asset.selected_root ? 'hidden selected root' : 'hidden variant')), asset.current_visible ? 'good' : 'warn'),
      pill(`support ${asset.direct_support}`), pill(`exact ${asset.exact_observations}`),
      ...(asset.timestamp_verified ? [pill(`clock ${asset.extracted_minute}`, 'good')] : [pill('clock unverified', 'warn')]),
      ...(asset.reclaimed_at ? [pill('reclaimed', 'bad')] : []),
    ];
    const children = [];
    if (asset.playable) {
      const video = node('video', { controls: '', preload: 'none', src: `/media/${asset.id}` });
      children.push(video);
    } else {
      children.push(node('div', { class: 'empty-state', text: 'Media reclaimed; SQL evidence remains.' }));
    }
    const body = node('div', { class: 'asset-body' }, [
      node('h3', { text: `#${index + 1} · ${asset.md5}` }),
      node('div', { class: 'pill-row' }, badges),
      node('div', { class: 'stats' }, [
        stat('resolution', `${asset.width}×${asset.height}`), stat('fps', asset.frame_rate ? asset.frame_rate.toFixed(2) : '—'),
        stat('duration', duration(asset.duration_ms)), stat('size', bytes(asset.file_size_bytes)),
        stat('bitrate', asset.bitrate ? `${Math.round(asset.bitrate / 1000)} kbps` : '—'), stat('dHash frames', asset.frame_count),
      ]),
      node('div', { class: 'event-meta', text: `asset ${asset.id}${asset.share_id ? ` · ${asset.share_id} (${asset.share_state})` : ' · no public share'}` }),
      ...(asset.superseded_by ? [node('div', { class: 'event-meta', text: `superseded by ${asset.superseded_by}` })] : []),
    ]);
    if (asset.validation) {
      body.append(node('details', {}, [node('summary', { text: 'Validation evidence' }), node('pre', { text: JSON.stringify(asset.validation, null, 2) })]));
    }
    children.push(body);
    return node('article', { class: `asset ${asset.current_visible ? 'public' : ''}` }, children);
  }

  function renderCandidate(candidate) {
    return node('div', { class: 'candidate' }, [
      node('div', { class: 'pill-row' }, [pill(candidate.outcome_class, candidate.outcome_class === 'failed' || candidate.outcome_class === 'rejected' ? 'bad' : 'good'), pill(`attempt ${candidate.search_attempt}`), ...(candidate.reject_reason ? [pill(candidate.reject_reason, 'warn')] : [])]),
      node('a', { href: candidate.tweet_url, target: '_blank', rel: 'noreferrer', text: candidate.tweet_url }),
      ...(candidate.tweet_text ? [node('p', { text: candidate.tweet_text })] : []),
      node('div', { class: 'event-meta', text: `observed ${candidate.observed_asset_id || 'none'} · credited ${candidate.credited_asset_id || 'none'}` }),
    ]);
  }

  function renderDetail(detail) {
    const event = detail.event;
    const fixture = detail.fixture;
    detailNode.className = 'detail';
    detailNode.replaceChildren();
    detailNode.append(node('header', { class: 'detail-head' }, [
      node('div', {}, [node('p', { class: 'eyebrow', text: `${event.event_type} · ${clock(event)}` }), node('h2', { text: event.player_name || event.detail }), node('p', { class: 'muted', text: `${fixture.home_team} – ${fixture.away_team} · ${fixture.league_name} · ${fixture.league_round}` })]),
      node('div', { class: 'score', text: `${fixture.home_score < 0 ? '–' : fixture.home_score} : ${fixture.away_score < 0 ? '–' : fixture.away_score}` }),
    ]));
    detailNode.append(node('div', { class: 'pill-row' }, [pill(fixture.state), pill(fixture.status_short), pill(`${detail.assets.length} accepted MD5s`), pill(`${detail.candidates.length} searched tweets`)]));
    const grid = node('div', { class: 'asset-grid section' });
    detail.assets.forEach((asset, index) => grid.append(renderAsset(asset, index)));
    if (!detail.assets.length) grid.append(node('p', { class: 'muted', text: 'No accepted video assets.' }));
    detailNode.append(grid);
    const candidates = node('section', { class: 'section' }, [node('h2', { text: 'Candidate provenance' })]);
    detail.candidates.forEach(candidate => candidates.append(renderCandidate(candidate)));
    detailNode.append(candidates);
    if (detail.selection_commits.length) {
      const receipts = node('section', { class: 'section' }, [node('h2', { text: 'Latest selection receipts' })]);
      detail.selection_commits.forEach(commit => receipts.append(node('details', {}, [node('summary', { text: `${new Date(commit.committed_at).toLocaleString()} · ${commit.id}` }), node('pre', { text: JSON.stringify({ policy: commit.policy, result: commit.result }, null, 2) })])));
      detailNode.append(receipts);
    }
  }

  async function selectEvent(id) {
    state.selected = id;
    for (const fixture of state.fixtures) {
      if (fixture.events.some(event => event.id === id)) state.expandedFixture = fixture.id;
    }
    renderFixtures();
    detailNode.className = 'detail empty';
    detailNode.replaceChildren(node('p', { class: 'muted', text: 'Loading event evidence…' }));
    const response = await fetch(`/api/events/${encodeURIComponent(id)}`);
    if (!response.ok) throw new Error(`event ${response.status}`);
    renderDetail(await response.json());
    state.detailDirty = false;
    refreshDetailNode.classList.add('hidden');
  }

  function markDetailDirty() {
    if (!state.selected) return;
    state.detailDirty = true;
    refreshDetailNode.classList.remove('hidden');
  }

  function scheduleRefresh() {
    clearTimeout(state.refreshTimer);
    state.refreshTimer = setTimeout(async () => {
      try {
        await loadFixtures(true);
        markDetailDirty();
        if (state.selected) connectionNode.textContent = 'live · selected evidence may have changed';
      } catch (error) {
        connectionNode.textContent = `refresh failed: ${error.message}`;
        connectionNode.classList.remove('live');
      }
    }, 150);
  }

  function connect() {
    const stream = new EventSource('/stream');
    stream.addEventListener('open', () => { connectionNode.textContent = 'live · PostgreSQL'; connectionNode.classList.add('live'); });
    stream.addEventListener('refresh', scheduleRefresh);
    stream.addEventListener('error', () => { connectionNode.textContent = 'reconnecting'; connectionNode.classList.remove('live'); });
  }

  document.querySelector('#filters').addEventListener('input', () => {
    clearTimeout(state.refreshTimer);
    state.refreshTimer = setTimeout(() => loadFixtures(true).catch(showError), 200);
  });
  moreNode.addEventListener('click', () => { state.offset += state.limit; loadFixtures(false).catch(showError); });
  refreshDetailNode.addEventListener('click', () => {
    if (state.selected) selectEvent(state.selected).catch(showDetailError);
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) scheduleRefresh(); });
  function showError(error) { fixturesNode.replaceChildren(node('p', { class: 'error', text: error.message })); }
  function showDetailError(error) {
    detailNode.className = 'detail empty';
    detailNode.replaceChildren(node('p', { class: 'error', text: `Unable to load event evidence: ${error.message}` }));
  }
  loadFixtures(true).catch(showError);
  connect();
})();
