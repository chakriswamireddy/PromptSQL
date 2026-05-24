"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { login } from "@/lib/auth";
import { ApiError } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ShieldCheck } from "lucide-react";

const LoginSchema = z.object({
  tenantSlug: z.string().min(1, "Tenant is required"),
  email: z.string().email("Invalid email"),
  password: z.string().min(1, "Password is required"),
  totpCode: z.string().length(6).optional().or(z.literal("")),
});

type LoginForm = z.infer<typeof LoginSchema>;

export default function LoginPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const returnTo = searchParams.get("returnTo") ?? "";

  const [serverError, setServerError] = useState<string | null>(null);
  const [needsMfa, setNeedsMfa] = useState(false);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginForm>({ resolver: zodResolver(LoginSchema) });

  async function onSubmit(data: LoginForm) {
    setServerError(null);
    try {
      await login({
        tenantSlug: data.tenantSlug,
        email: data.email,
        password: data.password,
        totpCode: data.totpCode || undefined,
      });
      const dest = returnTo.startsWith("/t/")
        ? returnTo
        : `/t/${data.tenantSlug}/policies`;
      router.replace(dest);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === "mfa_required") {
          setNeedsMfa(true);
          setServerError("MFA code required — enter your authenticator code.");
        } else {
          setServerError(err.message);
        }
      } else {
        setServerError("An unexpected error occurred.");
      }
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-muted/40">
      <Card className="w-full max-w-md">
        <CardHeader className="space-y-1 text-center">
          <div className="flex justify-center mb-2">
            <ShieldCheck className="h-10 w-10 text-primary" />
          </div>
          <CardTitle className="text-2xl">Admin Console</CardTitle>
          <CardDescription>Governance Platform — sign in to your tenant</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4" noValidate>
            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="tenantSlug">
                Tenant
              </label>
              <Input
                id="tenantSlug"
                placeholder="acme"
                autoComplete="organization"
                {...register("tenantSlug")}
              />
              {errors.tenantSlug && (
                <p className="text-xs text-destructive">{errors.tenantSlug.message}</p>
              )}
            </div>

            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="email">
                Email
              </label>
              <Input
                id="email"
                type="email"
                placeholder="admin@acme.test"
                autoComplete="email"
                {...register("email")}
              />
              {errors.email && (
                <p className="text-xs text-destructive">{errors.email.message}</p>
              )}
            </div>

            <div className="space-y-1">
              <label className="text-sm font-medium" htmlFor="password">
                Password
              </label>
              <Input
                id="password"
                type="password"
                autoComplete="current-password"
                {...register("password")}
              />
              {errors.password && (
                <p className="text-xs text-destructive">{errors.password.message}</p>
              )}
            </div>

            {needsMfa && (
              <div className="space-y-1">
                <label className="text-sm font-medium" htmlFor="totpCode">
                  Authenticator Code
                </label>
                <Input
                  id="totpCode"
                  type="text"
                  inputMode="numeric"
                  maxLength={6}
                  placeholder="123456"
                  autoComplete="one-time-code"
                  {...register("totpCode")}
                />
              </div>
            )}

            {serverError && (
              <p className="text-sm text-destructive bg-destructive/10 rounded-md px-3 py-2">
                {serverError}
              </p>
            )}

            <Button type="submit" className="w-full" disabled={isSubmitting}>
              {isSubmitting ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
