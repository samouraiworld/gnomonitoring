import React from "react";


type Webhook = { ID?: number; DESCRIPTION: string; URL: string; Type: string };
type WebhookType = "gov" | "val";
type WebhookField = keyof Webhook;
type ContactAlert = { ID?: number; MONIKER: string; NAME: string; MENTIONTAG: string };