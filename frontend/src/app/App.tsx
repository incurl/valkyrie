/**
 * The Valkyrie application shell.
 *
 * The layout is a left control rail, a centre canvas and a right
 * sidebar, with the job queue in the header area and the debug console
 * at the bottom.
 *
 * All backend access goes through the Wails bindings; the frontend never
 * talks to PocketBase or to a remote API directly.
 */

import { useCallback, useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Events } from '@wailsio/runtime';

import '../styles.css';

import * as api from '../bindings';
import type { Job } from '../bindings';
import { applyZoom } from '../ui/zoom';
import { ConsolePanel } from '../features/console/ConsolePanel';
import { JobQueue } from '../features/queue/JobQueue';
import { SettingsDialog } from '../features/settings/SettingsDialog';
import { SubmitPanel } from '../features/submit/SubmitPanel';

interface Toast {
  message: string;
  kind: 'info' | 'error' | 'success';
}

/**
 * The application root.
 */
function App() {
  const queryClient = useQueryClient();

  const [settingsOpen, setSettingsOpen] = useState(false);
  const [toast, setToast] = useState<Toast | null>(null);

  const notify = useCallback((message: string, kind: Toast['kind'] = 'info') => {
    setToast({ message, kind });
    window.setTimeout(() => setToast(null), 4000);
  }, []);

  // --- Data ---------------------------------------------------------

  const settingsQuery = useQuery({
    queryKey: ['settings'],
    queryFn: () => api.getSettings(),
  });

  const jobsQuery = useQuery({
    queryKey: ['jobs'],
    queryFn: () => api.listJobs(50),
    // The Wails event pushes updates; this interval is the fallback.
    refetchInterval: 5000,
  });

  const consoleQuery = useQuery({
    queryKey: ['console'],
    queryFn: () => api.listConsoleEntries(),
    refetchInterval: 5000,
  });

  // --- Live updates -------------------------------------------------

  // The job event keeps the queue in step with the backend without
  // polling. The refetch interval above is only the safety net, so a
  // missed event only delays a refresh.
  useEffect(() => {
    const unsubscribe = Events.On(api.EVENT_JOB_UPDATED, () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    });
    return () => {
      unsubscribe();
    };
  }, [queryClient]);

  useEffect(() => {
    const unsubscribe = Events.On(api.EVENT_CONSOLE_ENTRY, () => {
      void queryClient.invalidateQueries({ queryKey: ['console'] });
    });
    return () => {
      unsubscribe();
    };
  }, [queryClient]);

  // --- Actions ------------------------------------------------------

  const submitMutation = useMutation({
    mutationFn: (request: { prompt: string; params?: Record<string, unknown> }) =>
      api.submitJob(request),
    onSuccess: (job: Job) => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
      notify(`The job "${job.title}" was queued.`, 'success');
    },
    onError: (error: Error) => {
      notify(error.message, 'error');
    },
  });

  const cancelMutation = useMutation({
    mutationFn: (jobId: string) => api.cancelJob(jobId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    },
    onError: (error: Error) => {
      notify(error.message, 'error');
    },
  });

  const retryMutation = useMutation({
    mutationFn: (jobId: string) => api.retryJob(jobId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
      notify('The job was queued again.', 'success');
    },
    onError: (error: Error) => {
      notify(error.message, 'error');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (jobId: string) => api.deleteJob(jobId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['jobs'] });
    },
    onError: (error: Error) => {
      notify(error.message, 'error');
    },
  });

  const clearConsoleMutation = useMutation({
    mutationFn: () => api.clearConsoleEntries(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['console'] });
    },
  });

  // The zoom is restored as soon as the settings are known.
  useEffect(() => {
    if (settingsQuery.data) {
      applyZoom(settingsQuery.data.ui?.zoom ?? 100);
    }
  }, [settingsQuery.data]);

  const jobs = jobsQuery.data ?? [];
  const entries = consoleQuery.data ?? [];

  return (
    <div className="app">
      <header className="app-header">
        <span className="app-brand">
          Valkyrie
          <small>Wails v3 · PocketBase · React</small>
        </span>

        <span className="app-header-spacer" />

        <JobQueue
          jobs={jobs}
          onCancel={(jobId) => cancelMutation.mutate(jobId)}
          onRetry={(jobId) => retryMutation.mutate(jobId)}
          onDelete={(jobId) => deleteMutation.mutate(jobId)}
        />

        <button type="button" className="btn btn-sm" onClick={() => setSettingsOpen(true)}>
          Settings
        </button>
      </header>

      <aside className="app-rail">
        <SubmitPanel
          disabled={submitMutation.isPending}
          onSubmit={(prompt, params) => submitMutation.mutate({ prompt, params })}
        />
      </aside>

      <main className="app-canvas">
        <section className="panel">
          <div className="panel-header">
            <span>Getting started</span>
          </div>
          <div className="panel-body">
            <p className="text-dim">
              This is the Valkyrie scaffold. It ships a generic job queue, an
              embedded PocketBase and a React shell. Replace the runner in
              <code> internal/runner</code> with your own backend, then extend
              the panels to match.
            </p>
            <p className="text-dim">
              Submit a prompt on the left to see the whole pipeline work: the
              job is persisted, queued, executed by the runner and pushed back
              to this window as a Wails event.
            </p>
          </div>
        </section>
      </main>

      <aside className="app-gallery">
        <section className="panel">
          <div className="panel-header">
            <span>Connection</span>
          </div>
          <div className="panel-body">
            <div className="field">
              <span className="field-label">API key</span>
              <span className="text-dim">
                {settingsQuery.data?.has_api_key
                  ? 'Configured. It is stored encrypted and never sent back to the UI.'
                  : 'Not configured. Add one in Settings.'}
              </span>
            </div>
            <div className="field">
              <span className="field-label">Base URL</span>
              <span className="text-dim">{settingsQuery.data?.base_url ?? '—'}</span>
            </div>
          </div>
        </section>
      </aside>

      <ConsolePanel
        entries={entries}
        onClear={() => clearConsoleMutation.mutate()}
      />

      <SettingsDialog
        open={settingsOpen}
        settings={settingsQuery.data ?? null}
        onClose={() => setSettingsOpen(false)}
        onSave={(apiKey, baseUrl) => {
          void api.saveCredentials(apiKey, baseUrl).then(() => {
            void queryClient.invalidateQueries({ queryKey: ['settings'] });
            notify('The settings were saved.', 'success');
            setSettingsOpen(false);
          });
        }}
        onSaveUI={(ui) => {
          void api.saveUI(ui).then(() => {
            void queryClient.invalidateQueries({ queryKey: ['settings'] });
            applyZoom(ui.zoom);
          });
        }}
      />

      {toast && (
        <div className={`toast toast-${toast.kind}`} role="status">
          {toast.message}
        </div>
      )}
    </div>
  );
}

export default App;
