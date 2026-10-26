import { useLocation } from 'react-router-dom';

const PageBackground = () => {
  const { pathname } = useLocation();

  if (!pathname.startsWith('/TFYT')) return null;

  return (
    <div className='pointer-events-none fixed inset-0 -z-10 h-full w-full overflow-hidden bg-[#f7f8ff] dark:bg-[#090b12]'>
      <div className='tf-background-mesh absolute inset-0' />
      <div className='tf-background-grid absolute inset-0 opacity-35 dark:opacity-15' />
    </div>
  );
};

export default PageBackground;
