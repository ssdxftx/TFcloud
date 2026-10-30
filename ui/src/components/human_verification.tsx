import { Button } from '@heroui/button';
import { Input } from '@heroui/input';
import { useCallback, useEffect, useState } from 'react';
import { IoRefresh, IoShieldCheckmarkOutline } from 'react-icons/io5';

import { ApiError, apiRequest } from '@/api/client';
import type { HumanChallenge, HumanChallengeVerification } from '@/types/sign';

interface HumanVerificationProps {
  disabled?: boolean;
  purpose?: 'login' | 'register';
  onVerified: (challengeId: string | null) => void;
}

export default function HumanVerification ({ disabled = false, purpose = 'login', onVerified }: HumanVerificationProps) {
  const [challenge, setChallenge] = useState<HumanChallenge | null>(null);
  const [answer, setAnswer] = useState('');
  const [loading, setLoading] = useState(true);
  const [verifying, setVerifying] = useState(false);
  const [verified, setVerified] = useState(false);
  const [error, setError] = useState('');

  const issueChallenge = useCallback(async () => {
    setLoading(true);
    setVerified(false);
    setAnswer('');
    setError('');
    onVerified(null);
    try {
      const result = await apiRequest<HumanChallenge>({
        method: 'POST',
        url: '/auth/human-challenge',
        data: { purpose },
      });
      setChallenge(result);
    } catch (requestError) {
      setChallenge(null);
      setError(requestError instanceof Error ? requestError.message : '验证服务暂时不可用');
    } finally {
      setLoading(false);
    }
  }, [onVerified, purpose]);

  useEffect(() => { void issueChallenge(); }, [issueChallenge]);

  const verify = async () => {
    if (!challenge || disabled || verifying || verified) return;
    if (!answer.trim()) {
      setError('请输入图片中的验证码');
      return;
    }
    setVerifying(true);
    setError('');
    try {
      const result = await apiRequest<HumanChallengeVerification>({
        method: 'POST',
        url: '/auth/human-challenge/verify',
        data: { challenge_id: challenge.challenge_id, answer: answer.trim() },
      });
      setVerified(true);
      setAnswer('');
      onVerified(result.challenge_id);
    } catch (requestError) {
      const message = requestError instanceof Error ? requestError.message : '验证未完成，请重试';
      setError(message);
      setAnswer('');
      if (requestError instanceof ApiError && requestError.code === 'HUMAN_CHALLENGE_EXPIRED') {
        await issueChallenge();
      }
    } finally {
      setVerifying(false);
    }
  };

  return (
    <div className='space-y-2'>
      <div className='flex min-h-[72px] items-center justify-center rounded-2xl border border-default-200 bg-content1/60 p-2'>
        {loading && <span className='text-sm text-default-400'>正在生成验证码…</span>}
        {!loading && challenge && (
          <img
            alt='人机验证码'
            className='h-[72px] w-full max-w-[260px] select-none rounded-xl object-contain'
            draggable={false}
            src={challenge.captcha_image}
          />
        )}
        {!loading && !challenge && <span className='text-sm text-danger'>验证码加载失败</span>}
      </div>
      {!verified && challenge && (
        <div className='flex items-end gap-2'>
          <Input
            aria-label='图片验证码'
            autoComplete='off'
            className='flex-1'
            isDisabled={disabled || verifying}
            label='图片验证码'
            maxLength={5}
            placeholder='输入图片中的字符'
            value={answer}
            onValueChange={(value) => setAnswer(value.replace(/[^a-zA-Z0-9]/g, '').toUpperCase().slice(0, 5))}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault();
                void verify();
              }
            }}
          />
          <Button
            color='primary'
            isDisabled={disabled}
            isLoading={verifying}
            variant='flat'
            onPress={() => void verify()}
          >
            {verified ? '已通过' : '验证'}
          </Button>
        </div>
      )}
      <div className='flex min-h-5 items-center justify-between gap-2 px-1 text-xs'>
        <span className={error ? 'text-danger' : verified ? 'text-success' : 'text-default-400'}>
          {error || (verified ? `验证通过，本次验证仅可用于一次${purpose === 'register' ? '注册' : '登录'}` : '请输入图片中的字符，不区分大小写')}
        </span>
        {!loading && !verified && (
          <Button
            isIconOnly
            aria-label='刷新验证码'
            isDisabled={disabled || verifying}
            size='sm'
            variant='light'
            onPress={() => void issueChallenge()}
          >
            <IoRefresh />
          </Button>
        )}
      </div>
      {!verified && <div className='flex items-center gap-1 px-1 text-xs text-default-400'><IoShieldCheckmarkOutline /> 服务端校验，验证码 5 分钟内有效</div>}
    </div>
  );
}
