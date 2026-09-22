/**
 * Real-time transport manager for Halp: SSE with jittered backoff and idle demotion fallback.
 *
 * SPECIFICATION (docs/DELIVERY.md, docs/OPEN-QUESTIONS.md #36):
 * - Primary transport: SSE over GET /v1/stream.
 * - Heartbeats: event "ka" every 25 seconds.
 * - Arrivals: event "ping" with payload {"n": N}.
 * - Idle demotion: event "demote" after 15 minutes idle.
 * - On demotion: close SSE stream, fall back to polling GET /v1/pending.
 * - Reconnection: exponential backoff with randomized full jitter to prevent client synchronization.
 */

import { HalpClient } from './client.js';

export interface TransportEvents {
  onPing: (count: number) => void;
  onStatusChange?: (status: TransportStatus) => void;
  onError?: (err: Error) => void;
}

export type TransportStatus = 'disconnected' | 'connecting' | 'connected' | 'polling' | 'closed';

export interface TransportOptions {
  client: HalpClient;
  events: TransportEvents;
  baseDelayMs?: number;
  maxDelayMs?: number;
  pollIntervalMs?: number;
  eventSourceFactory?: (url: string) => EventSource;
}

/**
 * Calculates exponential backoff with randomized full jitter.
 * Formula: random(0, min(maxDelay, baseDelay * 2^attempt))
 */
export function calculateJitteredBackoff(
  attempt: number,
  baseDelayMs: number = 1000,
  maxDelayMs: number = 30000,
  randomFn: () => number = Math.random
): number {
  const cap = Math.min(maxDelayMs, baseDelayMs * Math.pow(2, Math.min(attempt, 10)));
  return Math.floor(randomFn() * cap);
}

export class TransportManager {
  private client: HalpClient;
  private events: TransportEvents;
  private status: TransportStatus = 'disconnected';
  private eventSource: EventSource | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private attempt = 0;
  private baseDelayMs: number;
  private maxDelayMs: number;
  private pollIntervalMs: number;
  private isClosed = false;
  private eventSourceFactory: (url: string) => EventSource;

  constructor(options: TransportOptions) {
    this.client = options.client;
    this.events = options.events;
    this.baseDelayMs = options.baseDelayMs ?? 1000;
    this.maxDelayMs = options.maxDelayMs ?? 30000;
    this.pollIntervalMs = options.pollIntervalMs ?? 60000;
    this.eventSourceFactory = options.eventSourceFactory ?? ((url: string) => new EventSource(url));
  }

  getStatus(): TransportStatus {
    return this.status;
  }

  private setStatus(newStatus: TransportStatus): void {
    if (this.status !== newStatus) {
      this.status = newStatus;
      this.events.onStatusChange?.(newStatus);
    }
  }

  /**
   * Starts real-time streaming connection over SSE.
   */
  start(): void {
    if (this.isClosed) return;
    this.stopPolling();
    this.connectSSE();
  }

  private connectSSE(): void {
    if (this.isClosed) return;
    this.cleanupSSE();

    const token = this.client.getSessionToken();
    if (!token) {
      this.setStatus('disconnected');
      return;
    }

    this.setStatus('connecting');

    // Build URL with session query if custom headers aren't supported by browser EventSource
    // Note: server/internal/http/router.go accepts Bearer token or cookie/query fallback for SSE
    const baseUrl = this.client.getBaseUrl();
    const sseUrl = `${baseUrl}/v1/stream`;

    try {
      const es = this.eventSourceFactory(sseUrl);
      this.eventSource = es;

      es.onopen = () => {
        this.attempt = 0;
        this.setStatus('connected');
      };

      // Heartbeat event "ka"
      es.addEventListener('ka', () => {
        // Active heartbeat received
      });

      // Ping arrival event "ping"
      es.addEventListener('ping', (event: MessageEvent) => {
        let count = 1;
        if (event.data) {
          try {
            const data = JSON.parse(event.data);
            if (typeof data.n === 'number') {
              count = data.n;
            }
          } catch {
            // default to count 1
          }
        }
        this.events.onPing(count);
      });

      // Server demoted idle connection: event "demote"
      es.addEventListener('demote', () => {
        this.cleanupSSE();
        this.startPolling();
      });

      es.onerror = () => {
        if (this.isClosed) return;
        this.cleanupSSE();
        this.scheduleReconnect();
      };
    } catch (err) {
      this.scheduleReconnect();
    }
  }

  private scheduleReconnect(): void {
    if (this.isClosed) return;
    this.setStatus('disconnected');

    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
    }

    const delay = calculateJitteredBackoff(this.attempt++, this.baseDelayMs, this.maxDelayMs);
    this.reconnectTimer = setTimeout(() => {
      this.connectSSE();
    }, delay);
  }

  /**
   * Transitions from SSE to polling /v1/pending upon demotion.
   */
  startPolling(): void {
    if (this.isClosed) return;
    this.setStatus('polling');

    this.pollPending();
    this.pollTimer = setInterval(() => {
      this.pollPending();
    }, this.pollIntervalMs);
  }

  private async pollPending(): Promise<void> {
    if (this.isClosed || this.status !== 'polling') return;
    try {
      const pending = await this.client.getPending();
      if (pending && pending.n > 0) {
        this.events.onPing(pending.n);
      }
    } catch (err) {
      this.events.onError?.(err instanceof Error ? err : new Error(String(err)));
    }
  }

  private stopPolling(): void {
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  private cleanupSSE(): void {
    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }
  }

  /**
   * Closes all connections and polling timers permanently.
   */
  close(): void {
    this.isClosed = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.stopPolling();
    this.cleanupSSE();
    this.setStatus('closed');
  }
}
