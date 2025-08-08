"use client";
import { useUser } from "@clerk/nextjs";
import { useEffect, useState } from "react";

type Webhook = { ID?: number; Description: string; URL: string; Type: string };
type WebhookType = "gov" | "val";
type WebhookField = keyof Webhook;
type ContactAlert = { ID?: number; MONIKER: string; NAME: string; MENTIONTAG: string; IDWEBHOOK?: number; };

export function ConfigBot() {
    const { user, isLoaded } = useUser();
    const [dailyHour, setDailyHour] = useState<number>(0);
    const [dailyMinute, setDailyMinute] = useState<number>(0);
    type Webhook = { ID?: number; Description: string; URL: string; Type: string };
    const [govWebhooks, setGovWebhooks] = useState<Webhook[]>([{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
    const [valWebhooks, setValWebhooks] = useState<Webhook[]>([{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
    const [contacts, setContacts] = useState<ContactAlert[]>([{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "", IDWEBHOOK: undefined }]);
    const sections: { title: string; type: WebhookType; webhooks: Webhook[] }[] = [
        { title: "Webhooks GovDAO", type: "gov", webhooks: govWebhooks },
        { title: "Webhooks Validator", type: "val", webhooks: valWebhooks },
    ];
    const loadConfig = async () => {

        if (!user) return; // ✅ Securety
        try {
            const res = await fetch(`/api/get-webhooks?user_id=${user.id}`);
            if (!res.ok) throw new Error("Error during the loading of the config");

            const data = await res.json();
            console.log("✅ Data reçue du backend :", data);

            setGovWebhooks(data.govWebhooks?.length > 0 ? data.govWebhooks : [{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
            setValWebhooks(data.valWebhooks?.length > 0 ? data.valWebhooks : [{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
            // setContacts(data.contacts?.length > 0 ? data.contacts : [{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "" }]);
            setContacts(
                data.contacts?.length > 0
                    ? data.contacts.map((c: any) => ({
                        ID: c.ID,
                        MONIKER: c.Moniker,
                        NAME: c.NameContact,
                        MENTIONTAG: c.MentionTag,
                        IDWEBHOOK: c.IDwebhook ? String(c.IDwebhook) : "",
                    }))
                    : [{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "", IDWEBHOOK: undefined }]
            );
            // ✅ UPdate hour if disponible 
            if (data.hour?.daily_report_hour !== undefined && data.hour?.daily_report_minute !== undefined) {
                setDailyHour(data.hour.daily_report_hour);
                setDailyMinute(data.hour.daily_report_minute);
            }
        } catch (err) {
            console.error("❌ Error during the loading of the config:", err);
        }
    };
    // Load ini of data 
    useEffect(() => {
        if (!isLoaded || !user) return;


        loadConfig();
    }, [isLoaded, user]);


    const handleWebhookChange = (type: WebhookType, index: number, field: WebhookField, value: string) => {
        const updater = type === "gov" ? setGovWebhooks : setValWebhooks;
        const current = type === "gov" ? govWebhooks : valWebhooks;

        const updated = [...current];
        updated[index] = { ...updated[index], [field]: value };
        updater(updated);
    };

    // ✅ add a wehbook (button Add)
    const handleAddWebhook = (type: WebhookType) => {
        if (type === "gov") {
            setGovWebhooks([...govWebhooks, { ID: undefined, Description: "", URL: "", Type: "discord" }]);
        } else {
            setValWebhooks([...valWebhooks, { ID: undefined, Description: "", URL: "", Type: "discord" }]);
        }
        return

    };

    // ✅ Save webhook info in the backend"Save"
    const handleSaveNewWebhook = async (type: WebhookType, index: number) => {
        const webhook = type === "gov" ? govWebhooks[index] : valWebhooks[index];
        const target = type === "gov" ? "govdao" : "validator";

        if (!webhook.URL.trim()) {
            alert("⚠️ L'url ne peut pas être vide !");
            return;
        }
        console.log("Description webhhok" + webhook.Description)
        try {
            const res = await fetch("/api/add-webhook", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    UserID: user?.id,
                    Description: webhook.Description,
                    URL: webhook.URL,
                    Type: webhook.Type,
                    target,
                }),
            });

            if (res.status === 409) {
                alert("⚠️ Ce webhook existe déjà !");
                return;
            }

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Erreur lors de l’enregistrement du webhook.");
            } else {
                const data = await res.json(); // supposons { id: 123 }
                alert("✅ Webhook enregistré avec succès !");
                await loadConfig();

                // ✅ Mettre à jour l'ID dans le state (important pour Delete)
                if (type === "gov") {
                    const updated = [...govWebhooks];
                    updated[index] = { ...updated[index], ID: data.id }; // ✅ Force la copie                    setGovWebhooks(updated);
                } else {
                    const updated = [...valWebhooks];
                    updated[index] = { ...updated[index], ID: data.id }; // ✅ Force la copie                    setValWebhooks(updated);
                }
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau lors de l’appel API.");
        }
    };
    const handleUpdateNewWebhook = async (type: WebhookType, index: number) => {
        const webhook = type === "gov" ? govWebhooks[index] : valWebhooks[index];
        const target = type === "gov" ? "govdao" : "validator";

        try {
            const res = await fetch("/api/edit-webhook", {
                method: "PUT",
                headers: {
                    "Content-Type": "application/json",
                },
                body: JSON.stringify({
                    UserID: user?.id,
                    ID: webhook.ID,
                    URL: webhook.URL,
                    Type: webhook.Type,
                    Description: webhook.Description,
                    target,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API Update:", errorText);
                alert("Erreur lors de la mise à jour du webhook.");
            } else {
                alert("✅ Webhook mis à jour avec succès !");
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau lors de l’appel API.");
        }
        ;
    };


    async function handleDeleteWebhook(type: WebhookType, id?: number) {
        if (!id || !user?.id) {
            alert("⚠️ Impossible de supprimer : ID ou user_id manquant");
            return;
        }

        try {
            const res = await fetch(`/api/delete-webhook`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    id,
                    user_id: user.id,
                    type: type === "gov" ? "govdao" : "validator",
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Network error while deleting");
                return;
            }

            // ✅ Reload List
            await loadConfig();

            // ✅ If the list is empty, we keep one line by default
            if (type === "gov" && govWebhooks.length <= 1) {
                setGovWebhooks([{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
            }
            if (type === "val" && valWebhooks.length <= 1) {
                setValWebhooks([{ ID: undefined, Description: "", URL: "", Type: "discord" }]);
            }

            alert("✅ Webhook successfully removed!");
        } catch (error) {
            console.error("❌  Network error:", error);
            alert("Network error while deleting");
        }
    }
    const handleCreateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.MONIKER || !contact.NAME || !contact.MENTIONTAG || !contact.IDWEBHOOK) {
            alert("⚠️ All fields must be completed.");
            return;
        }
        console.log("Saving contact with webhook ID:", contact.IDWEBHOOK, typeof contact.IDWEBHOOK);
        try {
            const res = await fetch("/api/add-contact-alert", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    user_id: user?.id,
                    moniker: contact.MONIKER,
                    namecontact: contact.NAME,
                    mention_tag: contact.MENTIONTAG,
                    id_Webhook: contact.IDWEBHOOK,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Error API :", errorText);
                alert("Error saving contact.");
            } else {
                alert("✅ Save Contact!");
                await loadConfig(); // Reload contacts with ID in BDD
            }
        } catch (error) {
            console.error("❌ Network error:", error);
            alert("Network error.");
        }
    };

    const handleUpdateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.ID) {
            alert("⚠️ This contact has not yet been saved.");
            return;
        }

        try {
            const res = await fetch("/api/edit-contact-alert", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    id: contact.ID,
                    moniker: contact.MONIKER,
                    namecontact: contact.NAME,
                    mention_tag: contact.MENTIONTAG,
                    id_webhook: contact.IDWEBHOOK,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Error API update:", errorText);
                alert("Error updating contact.");
            } else {
                alert("✅ Contact updated!");
            }
        } catch (error) {
            console.error("❌ Network error: :", error);
            alert("Network error.");
        }
    };


    const handleAddContact = () => {
        setContacts([...contacts, { MONIKER: "", NAME: "", MENTIONTAG: "" }]);
    };

    const handleContactChange = (i: number, field: string, value: string) => {
        const updated = [...contacts];
        updated[i] = {
            ...updated[i],
            [field]: field === "IDWEBHOOK" ? parseInt(value, 10) || undefined : value,
        };
        setContacts(updated);
    };

    async function handleDeleteContact(id?: number) {
        if (!id || !user?.id) {
            alert("⚠️ Impossible de supprimer : ID ou user_id manquant");
            return;
        }

        try {
            const res = await fetch(`/api/delete-contact-alert`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    id,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Erreur lors de la suppression");
                return;
            }

            // ✅ Recharge la liste
            await loadConfig();



            alert("✅ contact alert supprimé avec succès !");
        } catch (error) {
            console.error("❌ Erreur réseau:", error);
            alert("Erreur réseau lors de la suppression");
        }
    }
    const handleSaveHour = async () => {
        if (!user?.id) return;

        try {
            const res = await fetch("/api/update-report-hour", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    hour: dailyHour,
                    minute: dailyMinute,
                    user_id: user.id,
                }),
            });

            if (!res.ok) {
                const text = await res.text();
                console.error("❌ Erreur API :", text);
                alert("Erreur lors de l'enregistrement de l'heure.");
            } else {
                alert("✅ Heure enregistrée !");
            }
        } catch (err) {
            console.error("❌ Erreur réseau :", err);
            alert("Erreur réseau.");
        }
    };


    useEffect(() => {
        if (isLoaded && user) loadConfig();
    }, [isLoaded, user]);

    return {
        user,
        isLoaded,
        dailyHour,
        dailyMinute,
        setDailyHour,
        setDailyMinute,
        govWebhooks,
        valWebhooks,
        contacts,
        handleAddWebhook,
        handleWebhookChange,
        handleUpdateNewWebhook,
        setGovWebhooks,
        setValWebhooks,
        setContacts,
        loadConfig,
        handleSaveNewWebhook,
        handleDeleteWebhook,
        handleCreateContact,
        handleDeleteContact,
        handleUpdateContact,
        handleAddContact,
        handleContactChange,
        handleSaveHour,
        sections,


        // et toutes les autres fonctions exposées comme props (save, update, delete)
    };
}
