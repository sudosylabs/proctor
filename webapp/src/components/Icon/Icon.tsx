import {
  CircleAlert,
  CircleCheck,
  CircleX,
  Eye,
  EyeOff,
  Loader,
  Lock,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react";

import styles from "./Icon.module.css";

// The only place lucide-react is imported. Add a name only once a real use
// proves its meaning; directional glyphs need an RTL review first.
const iconComponents = {
  loading: Loader,
  hidePassword: EyeOff,
  showPassword: Eye,
  stateSuccess: CircleCheck,
  stateWarning: TriangleAlert,
  stateDanger: CircleX,
  error: CircleAlert,
  readOnly: Lock,
} satisfies Record<string, LucideIcon>;

export type IconName = keyof typeof iconComponents;
export type IconSize = "small" | "default" | "large";

export interface IconProps {
  className?: string;
  name: IconName;
  size?: IconSize;
}

/** Always decorative: the owner states the meaning in visible or accessible text. */
export function Icon({ className, name, size = "default" }: IconProps) {
  const IconComponent = iconComponents[name];
  const classes = [styles.icon, styles[size], className]
    .filter((value): value is string => value !== undefined)
    .join(" ");

  return (
    <IconComponent
      aria-hidden="true"
      className={classes}
      data-proctor-icon={name}
      focusable="false"
      strokeWidth={2}
    />
  );
}
