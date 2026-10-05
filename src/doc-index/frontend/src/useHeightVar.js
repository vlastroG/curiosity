import { useEffect, useRef } from 'react';

// useHeightVar держит в CSS-переменной на :root высоту элемента: по ней
// липкие блоки встают друг под другом, а прокрутка к сноске не прячет
// отрывок под ними.
export default function useHeightVar(name) {
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return undefined;
    const root = document.documentElement.style;
    const set = () => root.setProperty(name, `${Math.ceil(el.getBoundingClientRect().height)}px`);
    set();
    const ro = new ResizeObserver(set);
    ro.observe(el);
    window.addEventListener('resize', set);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', set);
      root.removeProperty(name);
    };
  }, [name]);
  return ref;
}
