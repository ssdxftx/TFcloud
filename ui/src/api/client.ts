import axios, { type AxiosRequestConfig } from 'axios';

import type { ApiEnvelope } from '@/types/sign';

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

function readCookie (name: string) {
  const prefix = `${name}=`;
  for (const part of document.cookie.split(';')) {
    const value = part.trim();
    if (value.startsWith(prefix)) return decodeURIComponent(value.slice(prefix.length));
  }
  return '';
}

client.interceptors.request.use((config) => {
  beginRequest(config);
  const method = String(config.method || 'get').toLowerCase();
  if (!['get', 'head', 'options'].includes(method)) {
    const csrf = readCookie('tf_csrf');
    if (csrf) config.headers['X-CSRF-Token'] = csrf;
  }
  return config;
});

// 清理旧版本遗留的 JS 可读 token；新会话只依赖 HttpOnly Cookie。
try {
  localStorage.removeItem('tf-sign-access-token');
} catch {
  // localStorage 不可用时忽略。
}

function isPublicAuthRequest (url?: string) {
  return Boolean(url && (url.startsWith('/auth/login') || url.startsWith('/auth/register') || url.startsWith('/auth/qq/') || url.startsWith('/auth/password/')));
}

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
    if (status === 401 && !isPublicAuthRequest(error.config?.url)) {
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
