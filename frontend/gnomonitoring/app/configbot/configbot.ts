"use client";
import { useUser } from "@clerk/nextjs";
import { useEffect, useState } from "react";

type Webhook = { ID?: number; DESCRIPTION: string; URL: string; Type: string };
type WebhookType = "gov" | "val";
type WebhookField = keyof Webhook;
type ContactAlert = { ID?: number; MONIKER: string; NAME: string; MENTIONTAG: string };

export function ConfigBot() {
    const { user, isLoaded } = useUser();
    const [dailyHour, setDailyHour] = useState<number>(0);
    const [dailyMinute, setDailyMinute] = useState<number>(0);
    const [govWebhooks, setGovWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [valWebhooks, setValWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [contacts, setContacts] = useState<ContactAlert[]>([{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "" }]);
    const sections: { title: string; type: WebhookType; webhooks: Webhook[] }[] = [
        { title: "Webhooks GovDAO", type: "gov", webhooks: govWebhooks },
        { title: "Webhooks Validator", type: "val", webhooks: valWebhooks },
    ];
    const loadConfig = async () => {

        if (!user) return; // ✅ Secxurety
        try {
            const res = await fetch(`/api/get-webhooks?user_id=${user.id}`);
            if (!res.ok) throw new Error("Error during the loading of the config");

            const data = await res.json();
            console.log("✅ Data reçue du backend :", data);

            setGovWebhooks(data.govWebhooks?.length > 0 ? data.govWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            setValWebhooks(data.valWebhooks?.length > 0 ? data.valWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            setContacts(data.contacts?.length > 0 ? data.contacts : [{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "" }]);

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
            setGovWebhooks([...govWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        } else {
            setValWebhooks([...valWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        }
        return

    };

    // ✅ Save webhook info in the backend"Save"
    const handleSaveNewWebhook = async (type: WebhookType, index: number) => {
        const webhook = type === "gov" ? govWebhooks[index] : valWebhooks[index];
        const target = type === "gov" ? "govdao" : "validator";

        if (!webhook.URL.trim()) {
            alert("⚠️ L'URL ne peut pas être vide !");
            return;
        }

        try {
            const res = await fetch("/api/add-webhook", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    user: user?.id,
                    description: webhook.DESCRIPTION,
                    url: webhook.URL,
                    type: webhook.Type,
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
                    user: user?.id,
                    id: webhook.ID,
                    url: webhook.URL,
                    type: webhook.Type,
                    description: webhook.DESCRIPTION,
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
                alert("Erreur lors de la suppression");
                return;
            }

            // ✅ Recharge la liste
            await loadConfig();

            // ✅ Si liste vide, on garde une ligne par défaut
            if (type === "gov" && govWebhooks.length <= 1) {
                setGovWebhooks([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            }
            if (type === "val" && valWebhooks.length <= 1) {
                setValWebhooks([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            }

            alert("✅ Webhook supprimé avec succès !");
        } catch (error) {
            console.error("❌ Erreur réseau:", error);
            alert("Erreur réseau lors de la suppression");
        }
    }
    const handleCreateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.MONIKER || !contact.NAME || !contact.MENTIONTAG) {
            alert("⚠️ Tous les champs doivent être remplis.");
            return;
        }

        try {
            const res = await fetch("/api/add-contact-alert", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    user_id: user?.id,
                    moniker: contact.MONIKER,
                    namecontact: contact.NAME,
                    mention_tag: contact.MENTIONTAG,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API :", errorText);
                alert("Erreur lors de l’enregistrement du contact.");
            } else {
                alert("✅ Contact enregistré !");
                await loadConfig(); // Recharge les contacts avec l’ID en BDD
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau.");
        }
    };

    const handleUpdateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.ID) {
            alert("⚠️ Ce contact n’a pas encore été enregistré.");
            return;
        }

        try {
            const res = await fetch("/api/edit-contact-alert", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    ID: contact.ID,
                    Moniker: contact.MONIKER,
                    NameContact: contact.NAME,
                    Mention_Tag: contact.MENTIONTAG,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API Update:", errorText);
                alert("Erreur lors de la mise à jour du contact.");
            } else {
                alert("✅ Contact mis à jour !");
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau.");
        }
    };


    const handleAddContact = () => {
        setContacts([...contacts, { MONIKER: "", NAME: "", MENTIONTAG: "" }]);
    };

    const handleContactChange = (i: number, field: string, value: string) => {
        const updated = [...contacts];
        updated[i] = { ...updated[i], [field]: value }; // FIX: maintenant ça met à jour la valeur
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
