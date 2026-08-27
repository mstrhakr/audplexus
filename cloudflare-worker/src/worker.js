/**
 * Audplexus Diagnostic Report Proxy
 *
 * Receives diagnostics reports from Audplexus, optionally creates a GitHub Gist,
 * and returns a pre-filled GitHub issue URL.
 */

export default {
  async fetch(request, env) {
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        headers: corsHeaders(),
      });
    }

    const url = new URL(request.url);
    if (url.pathname !== '/diagnostic' || request.method !== 'POST') {
      return jsonResponse({ success: false, error: 'Not found' }, 404);
    }

    try {
      const body = await request.json();
      const report = sanitizeReport(body?.report);
      if (!report || !report.report_id) {
        return jsonResponse({ success: false, error: 'Invalid report format' }, 400);
      }

      const uploadMode = String(report.upload_mode || 'gist_secret').toLowerCase();
      let gistURL = '';

      if (uploadMode !== 'none') {
        const gistResp = await fetch('https://api.github.com/gists', {
          method: 'POST',
          headers: {
            'Authorization': `Bearer ${env.GITHUB_PAT}`,
            'Accept': 'application/vnd.github+json',
            'User-Agent': 'Audplexus-Diagnostic-Proxy/1.0',
            'X-GitHub-Api-Version': '2022-11-28',
          },
          body: JSON.stringify({
            description: `Audplexus Diagnostics: ${report.issue_type || 'general'} ${report.timestamp || ''}`,
            public: uploadMode === 'gist_public',
            files: buildGistFiles(report),
          }),
        });

        if (!gistResp.ok) {
          const err = await gistResp.text();
          return jsonResponse({ success: false, error: `Failed to create gist: ${gistResp.status} ${err}` }, 502);
        }

        const gist = await gistResp.json();
        gistURL = gist.html_url || '';
      }

      const issueURL = buildIssueURL(env.GITHUB_REPO, report, gistURL);
      return jsonResponse({
        success: true,
        gist_url: gistURL,
        issue_url: issueURL,
      }, 200);
    } catch (err) {
      console.error('diagnostic worker request failed', err);
      return jsonResponse({ success: false, error: 'Internal server error' }, 500);
    }
  },
};

const URL_PATTERN = /https?:\/\/[^\s"'<>]+/g;
const IPV4_PATTERN = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
const SECRET_QUERY_PATTERN = /(api[_-]?key|token|secret)=([^&\s"']+)/gi;

function redactURLs(value) {
  if (typeof value !== 'string' || value.length === 0) return value;
  return value
    .replace(URL_PATTERN, '[redacted-url]')
    .replace(IPV4_PATTERN, '[redacted-ip]')
    .replace(SECRET_QUERY_PATTERN, '$1=[redacted]');
}

function sanitizeReport(report) {
  if (!report || typeof report !== 'object') return report;

  const out = { ...report };
  out.issue_title = redactURLs(String(out.issue_title || ''));
  out.user_message = redactURLs(String(out.user_message || ''));
  out.expected_value = redactURLs(String(out.expected_value || ''));
  out.repro_steps = redactURLs(String(out.repro_steps || ''));
  out.app_version = redactURLs(String(out.app_version || ''));
  out.deployment_mode = redactURLs(String(out.deployment_mode || ''));

  if (Array.isArray(out.recent_logs)) {
    out.recent_logs = out.recent_logs.map((line) => redactURLs(String(line || '')));
  }

  if (Array.isArray(out.destinations)) {
    out.destinations = out.destinations.map((d) => {
      if (!d || typeof d !== 'object') return d;
      const { url, ...rest } = d;
      if (typeof rest.health_detail === 'string') rest.health_detail = redactURLs(rest.health_detail);
      if (typeof rest.last_error === 'string') rest.last_error = redactURLs(rest.last_error);
      return rest;
    });
  }

  if (out.runtime_env && typeof out.runtime_env === 'object') {
    const logFile = out.runtime_env.log_file && typeof out.runtime_env.log_file === 'object'
      ? { ...out.runtime_env.log_file }
      : undefined;
    if (logFile) delete logFile.path;

    out.runtime_env = {
      go_version: out.runtime_env.go_version,
      os_arch: out.runtime_env.os_arch,
      num_cpu: out.runtime_env.num_cpu,
      log_level: out.runtime_env.log_level,
      authenticated: out.runtime_env.authenticated,
      marketplace: out.runtime_env.marketplace,
      last_sync: out.runtime_env.last_sync,
      server_time: out.runtime_env.server_time,
      log_file: logFile,
    };
  }

  return out;
}

function buildGistFiles(report) {
  const files = {};

  files['1_summary.md'] = {
    content: report.generated_summary || '# Audplexus Diagnostics\n\nNo summary supplied.',
  };

  const core = {
    report_id: report.report_id,
    timestamp: report.timestamp,
    issue_type: report.issue_type,
    expected_value: report.expected_value,
    repro_steps: report.repro_steps,
    user_message: report.user_message,
    issue_title: report.issue_title,
    app_version: report.app_version,
    deployment_mode: report.deployment_mode,
    range: report.range,
    mode: report.mode,
    detail: report.detail,
    log_source: report.log_source,
    logs_exported: report.logs_exported,
  };
  files['2_core_report.json'] = { content: JSON.stringify(core, null, 2) };

  if (Array.isArray(report.recent_logs) && report.recent_logs.length > 0) {
    files['3_recent_logs.txt'] = { content: report.recent_logs.join('\n') };
  }

  if (report.runtime_env && Object.keys(report.runtime_env).length > 0) {
    files['4_runtime_env.json'] = { content: JSON.stringify(report.runtime_env, null, 2) };
  }

  if (report.env_presence && Object.keys(report.env_presence).length > 0) {
    files['5_env_presence.json'] = { content: JSON.stringify(report.env_presence, null, 2) };
  }

  if (Array.isArray(report.destinations) && report.destinations.length > 0) {
    files['6_destinations.json'] = { content: JSON.stringify(report.destinations, null, 2) };
  }

  files['99_full_report.json'] = { content: JSON.stringify(report, null, 2) };
  return files;
}

function buildIssueURL(repoSlug, report, gistURL) {
  const [owner, repo] = String(repoSlug || 'mstrhakr/audplexus').split('/');
  const issueType = String(report.issue_type || 'diagnostics').toLowerCase();
  const customTitle = String(report.issue_title || '').trim();
  const generatedTitle = `diagnostics ${issueType} (${new Date().toISOString().slice(0, 10)})`;
  const title = ensureBugTitlePrefix(customTitle || generatedTitle);

  let area = 'Other';
  if (issueType.includes('sync')) area = 'Sync';
  else if (issueType.includes('download')) area = 'Downloads';
  else if (issueType.includes('metadata') || issueType.includes('tag')) area = 'Metadata / Tagging';
  else if (issueType.includes('destination')) area = 'Destinations (ABS / Plex / Emby / Jellyfin)';
  else if (issueType.includes('auth')) area = 'Authentication';
  else if (issueType.includes('ui')) area = 'UI / Dashboard';

  const expected = report.expected_value || 'See diagnostics notes below.';
  const actual = report.user_message
    ? String(report.user_message)
    : `Diagnostics handoff generated from Audplexus at ${report.timestamp || 'unknown time'}.`;
  const steps = String(report.repro_steps || '').trim();

  const notesLines = [
    `Issue type: ${report.issue_type || 'diagnostics'}`,
    `Range: ${report.range || '24h'}`,
    `Mode/Detail: ${report.mode || 'package'}/${report.detail || 'standard'}`,
  ];
  if (report.user_message) {
    notesLines.push('');
    notesLines.push('Reporter notes:');
    notesLines.push(String(report.user_message));
  }
  if (gistURL) {
    notesLines.push('');
    notesLines.push(`Artifact: ${gistURL}`);
  }

  const base = `https://github.com/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/issues/new`;
  const q = new URLSearchParams();
  q.set('template', 'bug_report.yml');
  q.set('title', title);
  q.set('summary', title.replace(/^bug:\s*/i, '').trim());
  q.set('area', area);
  if (steps) q.set('steps', steps);
  q.set('expected', expected);
  q.set('actual', actual);
  q.set('version', String(report.app_version || '').trim());
  q.set('diagnostics_url', gistURL || '');
  q.set('diagnostics_notes', notesLines.join('\n'));
  const envLines = [];
  if (report.runtime_env && typeof report.runtime_env === 'object') {
    if (report.runtime_env.os_arch) envLines.push(`OS/Arch: ${report.runtime_env.os_arch}`);
    if (report.runtime_env.go_version) envLines.push(`Go: ${report.runtime_env.go_version}`);
    if (report.runtime_env.marketplace) envLines.push(`Marketplace: ${report.runtime_env.marketplace}`);
    if (typeof report.runtime_env.authenticated !== 'undefined') envLines.push(`Authenticated: ${String(report.runtime_env.authenticated)}`);
  }
  const deployMode = String(report.deployment_mode || '').trim();
  if (deployMode) envLines.push(`Deployment: ${deployMode}`);
  q.set('env', envLines.length > 0 ? envLines.join('\n') : 'Environment details to be confirmed by reporter.');
  return `${base}?${q.toString()}`;
}

function ensureBugTitlePrefix(title) {
  const t = String(title || '').trim();
  if (t.length === 0) return 'bug: diagnostics';
  if (/^bug:\s*/i.test(t)) return t;
  return `bug: ${t}`;
}

function jsonResponse(data, status) {
  return new Response(JSON.stringify(data), {
    status,
    headers: {
      'Content-Type': 'application/json',
      ...corsHeaders(),
    },
  });
}

function corsHeaders() {
  return {
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Methods': 'POST, OPTIONS',
    'Access-Control-Allow-Headers': 'Content-Type',
    'Access-Control-Max-Age': '86400',
  };
}
