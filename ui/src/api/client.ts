import axios, { type AxiosRequestConfig } from 'axios';

import type { ApiEnvelope } from '@/types/sign';

export const AUTH_TOKEN_KEY = 'tf-sign-access-token';
export const AUTH_EXPIRED_EVENT = 'tf-sign-auth-expired';
export const GLOBAL_LOADING_EVENT = 'tf-global-loading';

let activeRequests = 0;
const trackedRequests = new WeakSet<object>();

export function getActiveRequestCount () {
  return activeRequests;
}

function emitLoadingState () {
  window.dispatchEvent(new CustomEvent(GLOBAL_LOADING_EVENT, {
    detail: { active: activeRequests },
  }));
}

function beginRequest (config: object) {
  if (trackedRequests.has(config)) return;
  trackedRequests.add(config);
  activeRequests += 1;
  emitLoadingState();
}

function finishRequest (config?: object) {
  if (!config || !trackedRequests.has(config)) return;
  trackedRequests.delete(config);
  activeRequests = Math.max(0, activeRequests - 1);
  emitLoadingState();
}

export class ApiError extends Error {
  constructor (
    message: string,
    public readonly code = 'REQUEST_FAILED',
    public readonly status = 0,
    public readonly requestId = ''
  ) {
    super(message);
  }
}

const client = axios.create({
  baseURL: import.meta.env.VITE_API_BASE_URL || '/api',
  timeout: 30000,
  withCredentials: true,
  headers: { Accept: 'application/json' },
});

function redirectToInstaller (code?: string) {
  if (code === 'INSTALL_REQUIRED' && window.location.pathname !== '/install') {
    window.location.replace('/install');
  }
}

client.interceptors.request.use((config) => {
  beginRequest(config);
  const token = localStorage.getItem(AUTH_TOKEN_KEY);
  if (token) config.headers.Authorization = `Bearer ${token}`;
  return config;
});

client.interceptors.response.use(
  (response) => {
    finishRequest(response.config);
    return response;
  },
  (error) => {
    finishRequest(error.config);
    const status = Number(error.response?.status || 0);
    const envelope = error.response?.data as Partial<ApiEnvelope<unknown>> | undefined;
    redirectToInstaller(envelope?.code);
    if (status === 401 && localStorage.getItem(AUTH_TOKEN_KEY)) {
      localStorage.removeItem(AUTH_TOKEN_KEY);
      window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT));
    }
    return Promise.reject(new ApiError(
      envelope?.message || error.message || '请求失败',
      envelope?.code || 'REQUEST_FAILED',
      status,
      envelope?.request_id || ''
    ));
  }
);

export async function apiRequest<T> (config: AxiosRequestConfig): Promise<T> {
  const response = await client.request<ApiEnvelope<T>>(config);
  if (response.data.code !== 'SUCCESS') {
    redirectToInstaller(response.data.code);
    throw new ApiError(response.data.message, response.data.code, response.status, response.data.request_id);
  }
  return response.data.data;
}

export default client;
