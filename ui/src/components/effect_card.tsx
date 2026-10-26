import { Card, CardProps } from '@heroui/card';
import clsx from 'clsx';
import React from 'react';

export interface HoverEffectCardProps extends CardProps {
  children: React.ReactNode
  maxXRotation?: number
  maxYRotation?: number
  lightClassName?: string
  lightStyle?: React.CSSProperties
}

const HoverEffectCard: React.FC<HoverEffectCardProps> = (props) => {
  const {
    children,
    className,
    maxXRotation,
    maxYRotation,
    lightClassName,
    lightStyle,
    ...cardProps
  } = props;
  void maxXRotation;
  void maxYRotation;
  void lightClassName;
  void lightStyle;

  return (
    <Card {...cardProps} className={clsx('relative overflow-hidden', className)}>
      {children}
    </Card>
  );
};

export default HoverEffectCard;
