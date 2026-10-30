import type * as React from "react";
import { cn } from "@/lib/cn";

/** A register table: ruled rows, small-capital column heads. */
export function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div className="relative w-full overflow-x-auto">
      <table className={cn("w-full border-collapse text-base", className)} {...props} />
    </div>
  );
}

export function Th({ className, ...props }: React.ComponentProps<"th">) {
  return (
    <th
      scope="col"
      className={cn("label border-b border-rule-strong px-2 py-1 text-left", className)}
      {...props}
    />
  );
}

export function Td({ className, ...props }: React.ComponentProps<"td">) {
  return <td className={cn("border-b border-rule px-2 py-2 align-top", className)} {...props} />;
}
