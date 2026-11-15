import {
  forwardRef,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";

import { Icon } from "../Icon/Icon";
import styles from "./InputField.module.css";

export interface InputFieldProps
  extends Omit<
    InputHTMLAttributes<HTMLInputElement>,
    "aria-describedby" | "aria-invalid" | "children" | "id"
  > {
  className?: string;
  describedBy?: string;
  description?: ReactNode;
  errorMessage?: ReactNode;
  id: string;
  inputClassName?: string;
  label: ReactNode;
  labelAccessory?: ReactNode;
  trailingControl?: ReactNode;
}

export const InputField = forwardRef<HTMLInputElement, InputFieldProps>(
  function InputField(
    {
      className,
      describedBy,
      description,
      errorMessage,
      id,
      inputClassName,
      label,
      labelAccessory,
      required = false,
      trailingControl,
      type = "text",
      ...inputProps
    },
    ref,
  ) {
    const descriptionID = description === undefined ? undefined : `${id}-description`;
    const errorID = errorMessage === undefined ? undefined : `${id}-error`;
    const descriptionReferences = [descriptionID, describedBy, errorID]
      .filter((value): value is string => value !== undefined)
      .join(" ");

    return (
      <div className={classes(styles.field, className)}>
        <div className={styles.labelRow}>
          <label htmlFor={id}>{label}</label>
          {labelAccessory === undefined ? null : (
            <div className={styles.labelAccessory}>{labelAccessory}</div>
          )}
        </div>
        <div className={styles.control}>
          <input
            {...inputProps}
            ref={ref}
            aria-describedby={
              descriptionReferences === "" ? undefined : descriptionReferences
            }
            aria-invalid={errorMessage === undefined ? undefined : true}
            className={classes(
              styles.input,
              trailingControl === undefined && inputProps.readOnly !== true
                ? undefined
                : styles.withTrailingControl,
              inputClassName,
            )}
            id={id}
            required={required}
            type={type}
          />
          {trailingControl}
          {inputProps.readOnly === true && trailingControl === undefined ? (
            <span className={styles.readOnlyMark} data-proctor-read-only="">
              <Icon name="readOnly" size="default" />
            </span>
          ) : null}
        </div>
        {description === undefined ? null : (
          <p className={styles.description} id={descriptionID}>
            {description}
          </p>
        )}
        {errorMessage === undefined ? null : (
          <p className={styles.error} id={errorID}>
            {errorMessage}
          </p>
        )}
      </div>
    );
  },
);

function classes(...values: Array<string | undefined>) {
  return values.filter((value): value is string => value !== undefined).join(" ");
}
