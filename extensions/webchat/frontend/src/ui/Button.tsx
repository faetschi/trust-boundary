import type { ButtonHTMLAttributes } from "react";
import { cx } from "./cx";
import styles from "./Button.module.css";

export type ButtonVariant = "default" | "outline" | "ghost";
export type ButtonSize = "default" | "sm" | "icon";

type ButtonProps = Omit<ButtonHTMLAttributes<HTMLButtonElement>, "type"> & {
  readonly variant?: ButtonVariant;
  readonly size?: ButtonSize;
};

// The one button. Variant and size are data attributes, so no class name depends on them. It is always type="button":
// nothing in this client submits a form. A caller passes className only for placement (position, margin), never for colour.
export function Button({ variant = "default", size = "default", className, ...rest }: ButtonProps) {
  return <button type="button" className={cx(styles.button, className)} data-variant={variant} data-size={size} {...rest} />;
}
