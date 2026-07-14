import { useState } from 'react';

interface Props {
  title: string;
}

export function Widget({ title }: Props) {
  const [count, setCount] = useState(0);
  return <button onClick={() => setCount(count + 1)}>{title}: {count}</button>;
}

export const Badge = (props: Props) => <span>{props.title}</span>;
