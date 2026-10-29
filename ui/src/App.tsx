import { Spinner } from '@heroui/spinner';
import { lazy, Suspense, useEffect } from 'react';
import { Link, Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom';

import { AUTH_TOKEN_KEY } from '@/api/client';
import { ConfirmCardHost } from '@/components/confirm_card';
import PageBackground from '@/components/page_background';
import PageLoadingBar from '@/components/page_loading_bar';
import Toaster from '@/components/toaster';
import { AuthProvider, useSignAuth } from '@/contexts/auth';
import { SiteProvider, useSite } from '@/contexts/site';
import SignLayout from '@/layouts/sign_layout';

const HomePage = lazy(() => import('@/pages/home'));
const LoginPage = lazy(() => import('@/pages/web_login'));
const RegisterPage = lazy(() => import('@/pages/web_register'));
const QqRegisterPage = lazy(() => import('@/pages/qq_register'));
const ForgotPasswordPage = lazy(() => import('@/pages/forgot_password'));
const InstallPage = lazy(() => import('@/pages/install'));
const DashboardPage = lazy(() => import('@/pages/sign/dashboard'));
const AccountsPage = lazy(() => import('@/pages/sign/accounts'));
const TasksPage = lazy(() => import('@/pages/sign/tasks'));
const TaskDetailPage = lazy(() => import('@/pages/sign/task_detail'));
const PluginsPage = lazy(() => import('@/pages/sign/plugins'));
const ProfilePage = lazy(() => import('@/pages/sign/profile'));
const AssistantPage = lazy(() => import('@/pages/sign/assistant'));
const AdminLayoutPage = lazy(() => import('@/pages/sign/admin/layout'));
const AdminOverviewPage = lazy(() => import('@/pages/sign/admin/overview'));
const AdminUsersPage = lazy(() => import('@/pages/sign/admin/users'));
const AdminAccountsPage = lazy(() => import('@/pages/sign/admin/accounts'));
const AdminTasksPage = lazy(() => import('@/pages/sign/admin/tasks'));
const AdminAuditPage = lazy(() => import('@/pages/sign/admin/audit'));
const AdminSettingsPage = lazy(() => import('@/pages/sign/admin/settings'));
const AdminVersionPage = lazy(() => import('@/pages/sign/admin/version'));
const AdminMailPage = lazy(() => import('@/pages/sign/admin/mail'));
const NotFoundPage = lazy(() => import('@/pages/not_found'));

function LoadingScreen ({ label = '正在加载天方云签' }: { label?: string }) {
  return <div className='flex min-h-screen items-center justify-center bg-background'><Spinner size='lg' label={label} /></div>;
}

// 空闲时预取主要页面 chunk：首次点击导航只需付一次 API 往返，
// 不必再等「下载 chunk」+「请求数据」两段串行。
function usePrefetchRoutes () {
  useEffect(() => {
    const run = () => {
      void import('@/pages/sign/dashboard');
      void import('@/pages/sign/accounts');
      void import('@/pages/sign/tasks');
      void import('@/pages/sign/profile');
      void import('@/pages/sign/plugins');
    };
    const ric = (window as unknown as { requestIdleCallback?: (cb: () => void, opts?: { timeout: number }) => number }).requestIdleCallback;
    const id = ric ? ric(run, { timeout: 3000 }) : window.setTimeout(run, 1500);
    return () => {
      const cic = (window as unknown as { cancelIdleCallback?: (id: number) => void }).cancelIdleCallback;
      if (ric && cic) cic(id); else window.clearTimeout(id);
    };
  }, []);
}

function ProtectedRoute () {
  const { loading, isAuthenticated } = useSignAuth();
  const location = useLocation();
  // 本地已有 token 时先放行渲染，让页面的数据请求与 /me 并行，
  // 避免在慢链路上串行等待两个往返（首屏能快接近一半）。
  const hasToken = Boolean(localStorage.getItem(AUTH_TOKEN_KEY));
  if (loading && !hasToken) return <LoadingScreen />;
  if (!loading && !isAuthenticated) return <Navigate to='/login' replace state={{ from: location.pathname }} />;
  return <Outlet />;
}

function AdminRoute () {
  const { loading, user } = useSignAuth();
  // 深链直接进后台时必须先确认角色，避免渲染后再弹回
  if (loading) return <LoadingScreen />;
  return user?.role === 'admin' ? <Outlet /> : <Navigate to='/TFYT' replace />;
}

function LegacyConsoleRedirect () {
  const location = useLocation();
  const legacyPath = location.pathname.startsWith('/console')
    ? location.pathname.slice('/console'.length)
    : location.pathname;
  return <Navigate to={`/TFYT${legacyPath}${location.search}`} replace />;
}

const maintenanceAllowedPaths = ['/login', '/register', '/qq-register', '/forgot-password', '/install'];

function MaintenanceGate ({ children }: { children: React.ReactNode }) {
  const site = useSite();
  const { user } = useSignAuth();
  const location = useLocation();
  if (!site.maintenance_mode || user?.role === 'admin' || maintenanceAllowedPaths.some((path) => location.pathname.startsWith(path))) {
    return <>{children}</>;
  }
  return (
    <div className='flex min-h-screen items-center justify-center bg-background px-6 text-foreground'>
      <div className='max-w-md rounded-[28px] border border-warning/25 bg-content1 p-8 text-center shadow-xl'>
        <p className='text-xs font-semibold tracking-[0.2em] text-warning'>MAINTENANCE</p>
        <h1 className='mt-3 text-2xl font-semibold'>{site.name || '天方云签'} 正在维护</h1>
        <p className='mt-3 text-sm leading-6 text-default-500'>站点暂时停止对外服务，维护完成后即可恢复。管理员仍可登录后台处理事务。</p>
        <Link to='/login' className='mt-6 inline-flex rounded-xl bg-primary px-4 py-2 text-sm font-medium text-white'>管理员登录</Link>
      </div>
    </div>
  );
}

export default function App () {
  usePrefetchRoutes();
  return (
    <SiteProvider>
    <AuthProvider>
      <PageBackground />
      <PageLoadingBar />
      <Toaster />
      <ConfirmCardHost />
      <MaintenanceGate>
      <Suspense fallback={<LoadingScreen />}>
        <Routes>
            <Route path='/' element={<HomePage />} />
            <Route path='/install' element={<InstallPage />} />
            <Route path='/login' element={<LoginPage />} />
            <Route path='/register' element={<RegisterPage />} />
            <Route path='/qq-register' element={<QqRegisterPage />} />
            <Route path='/forgot-password' element={<ForgotPasswordPage />} />
            <Route path='/web_login' element={<Navigate to='/login' replace />} />
            <Route element={<ProtectedRoute />}>
              <Route path='/TFYT' element={<SignLayout />}>
                <Route index element={<DashboardPage />} />
                <Route path='accounts' element={<AccountsPage />} />
                <Route path='tasks' element={<TasksPage />} />
                <Route path='tasks/:taskNo' element={<TaskDetailPage />} />
                <Route path='plugins' element={<Navigate to='/TFYT/accounts' replace />} />
                <Route path='profile' element={<ProfilePage />} />
                <Route path='assistant' element={<AssistantPage />} />
                <Route path='qq_login' element={<Navigate to='/TFYT/accounts' replace />} />
                <Route element={<AdminRoute />}>
                  <Route path='admin' element={<AdminLayoutPage />}>
                    <Route index element={<AdminOverviewPage />} />
                    <Route path='users' element={<AdminUsersPage />} />
                    <Route path='accounts' element={<AdminAccountsPage />} />
                    <Route path='tasks' element={<AdminTasksPage />} />
                    <Route path='tasks/:taskNo' element={<TaskDetailPage admin />} />
                    <Route path='audit' element={<AdminAuditPage />} />
                    <Route path='settings' element={<AdminSettingsPage />} />
                    <Route path='version' element={<AdminVersionPage />} />
                    <Route path='mail' element={<AdminMailPage />} />
                    <Route path='plugins' element={<PluginsPage />} />
                    <Route path='*' element={<NotFoundPage />} />
                  </Route>
                </Route>
                <Route path='*' element={<NotFoundPage />} />
              </Route>
            </Route>
            <Route path='/console/*' element={<LegacyConsoleRedirect />} />
            <Route path='/accounts' element={<LegacyConsoleRedirect />} />
            <Route path='/tasks/*' element={<LegacyConsoleRedirect />} />
            <Route path='/plugins' element={<LegacyConsoleRedirect />} />
            <Route path='/profile' element={<LegacyConsoleRedirect />} />
            <Route path='/assistant' element={<LegacyConsoleRedirect />} />
            <Route path='/qq_login' element={<LegacyConsoleRedirect />} />
            <Route path='/admin/*' element={<LegacyConsoleRedirect />} />
            <Route path='*' element={<NotFoundPage />} />
        </Routes>
      </Suspense>
      </MaintenanceGate>
    </AuthProvider>
    </SiteProvider>
  );
}
